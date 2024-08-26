package dra

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/Mellanox/rdmamap"
	"github.com/aojea/kubernetes-network-driver/pkg/hostdevice"
	"github.com/vishvananda/netlink"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/stub"

	"cloud.google.com/go/compute/metadata"

	resourceapi "k8s.io/api/resource/v1alpha3"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/klog/v2"
	drapb "k8s.io/kubelet/pkg/apis/dra/v1alpha4"
)

type storage struct {
	mu    sync.RWMutex
	cache map[types.UID]resourceapi.AllocationResult
}

func (s *storage) Add(uid types.UID, allocation resourceapi.AllocationResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache[uid] = allocation
}

func (s *storage) Get(uid types.UID) (resourceapi.AllocationResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	allocation, ok := s.cache[uid]
	return allocation, ok
}

func (s *storage) Remove(uid types.UID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cache, uid)
}

var _ drapb.NodeServer = &NetworkPlugin{}

type NetworkPlugin struct {
	driverName string
	kubeClient kubernetes.Interface
	draPlugin  kubeletplugin.DRAPlugin
	nriPlugin  stub.Stub

	podAllocations   storage
	claimAllocations storage

	ifaceGw string
}

func Start(ctx context.Context, driverName string, kubeClient kubernetes.Interface, nodeName string) (*NetworkPlugin, error) {
	plugin := &NetworkPlugin{
		driverName:       driverName,
		kubeClient:       kubeClient,
		podAllocations:   storage{cache: make(map[types.UID]resourceapi.AllocationResult)},
		claimAllocations: storage{cache: make(map[types.UID]resourceapi.AllocationResult)},
	}

	pluginRegistrationPath := "/var/lib/kubelet/plugins_registry/" + driverName + ".sock"
	driverPluginPath := "/var/lib/kubelet/plugins/" + driverName
	err := os.MkdirAll(driverPluginPath, 0750)
	if err != nil {
		return nil, fmt.Errorf("failed to create plugin path %s: %v", driverPluginPath, err)
	}
	driverPluginSocketPath := driverPluginPath + "/plugin.sock"

	ifaceGw, err := getDefaultGwIf()
	if err != nil {
		return nil, fmt.Errorf("failed to get interface for the default route: %v", err)
	}
	plugin.ifaceGw = ifaceGw

	nriOpts := []stub.Option{
		stub.WithPluginName(driverName),
		stub.WithPluginIdx("00"),
	}

	stub, err := stub.New(plugin, nriOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create plugin stub: %v", err)
	}

	plugin.nriPlugin = stub

	// cancel the plugin if the nri plugin fails for any reason
	inCtx, cancel := context.WithCancel(ctx)
	go func() {
		defer cancel()
		err = plugin.nriPlugin.Run(inCtx)
		if err != nil {
			klog.Infof("NRI plugin failed with error %v", err)
		}
	}()

	opts := []kubeletplugin.Option{
		kubeletplugin.DriverName(driverName),
		kubeletplugin.NodeName(nodeName),
		kubeletplugin.KubeClient(kubeClient),
		kubeletplugin.RegistrarSocketPath(pluginRegistrationPath),
		kubeletplugin.PluginSocketPath(driverPluginSocketPath),
		kubeletplugin.KubeletPluginSocketPath(driverPluginSocketPath),
	}
	d, err := kubeletplugin.Start(inCtx, plugin, opts...)
	if err != nil {
		return nil, fmt.Errorf("start kubelet plugin: %w", err)
	}
	plugin.draPlugin = d
	err = wait.PollUntilContextTimeout(inCtx, 1*time.Second, 30*time.Second, true, func(context.Context) (bool, error) {
		status := plugin.draPlugin.RegistrationStatus()
		if status == nil {
			return false, nil
		}
		return status.PluginRegistered, nil
	})
	if err != nil {
		return nil, err
	}
	// publish available resources
	go plugin.PublishResources(inCtx)
	return plugin, nil
}

func (np *NetworkPlugin) Stop() {
	np.nriPlugin.Stop()
	np.draPlugin.Stop()
}

func (np *NetworkPlugin) RunPodSandbox(_ context.Context, pod *api.PodSandbox) error {
	klog.V(2).Infof("RunPodSandbox pod %s/%s", pod.Namespace, pod.Name)

	allocation, ok := np.podAllocations.Get(types.UID(pod.Uid))
	if !ok {
		klog.V(2).Infof("RunPodSandbox pod %s/%s does not have allocations", pod.Namespace, pod.Name)
		return nil
	}

	// get the pod network namespace
	var ns string
	for _, namespace := range pod.Linux.GetNamespaces() {
		if namespace.Type == "network" {
			ns = namespace.Path
			break
		}
	}
	// TODO check host network namespace
	if ns == "" {
		klog.V(2).Infof("RunPodSandbox pod %s/%s using host network, skipping", pod.Namespace, pod.Name)
		return nil
	}

	for _, config := range allocation.Devices.Config {
		if config.Opaque == nil {
			continue
		}
		// TODO config.Request seems to be a sort of filter
		klog.Infof("RunPodSandbox config.Opaque.Parameters: %s", config.Opaque.Parameters.String())
		// TODO get config options here, it can add ips or commands
		// to add routes, run dhcp, rename the interface ... whatever

	}

	// attach the network devices to the pod namespace
	for _, result := range allocation.Devices.Results {
		klog.Infof("RunPodSandbox allocation.Devices.Result: %#v", result)
		err := hostdevice.MoveLinkIn(result.Device, ns, result.Device)
		if err != nil {
			klog.Infof("RunPodSandbox error moving device %s to namespace %s: %v", result.Device, ns, err)
			return err
		}
		rdmaDev, err := rdmamap.GetRdmaDeviceForNetdevice(result.Device)
		if err != nil {
			klog.Infof("RunPodSandbox error getting RDMA device %s to namespace %s: %v", result.Device, ns, err)
			continue
		}
		// TODO signal this via DRA
		if rdmaDev != "" {
			err = hostdevice.MoveRDMALinkIn(rdmaDev, ns)
			if err != nil {
				klog.Infof("RunPodSandbox error getting RDMA device %s to namespace %s: %v", result.Device, ns, err)
				continue
			}
		}
	}
	return nil
}

func (np *NetworkPlugin) StopPodSandbox(ctx context.Context, pod *api.PodSandbox) error {
	klog.V(2).Infof("StopPodSandbox pod %s/%s", pod.Namespace, pod.Name)
	allocation, ok := np.podAllocations.Get(types.UID(pod.Uid))
	if !ok {
		klog.V(2).Infof("StopPodSandbox pod %s/%s does not have allocations", pod.Namespace, pod.Name)
		return nil
	}
	defer np.podAllocations.Remove(types.UID(pod.Uid))

	// get the pod network namespace
	var ns string
	for _, namespace := range pod.Linux.GetNamespaces() {
		if namespace.Type == "network" {
			ns = namespace.Path
			break
		}
	}
	// TODO check host network namespace
	if ns == "" {
		return nil
	}

	// release the network devices from the pod namespace
	for _, config := range allocation.Devices.Config {
		if config.Opaque == nil {
			continue
		}
		// TODO config.Request seems to be a sort of filter
		klog.Infof("StopPodSandbox config.Opaque.Parameters: %s", config.Opaque.Parameters.String())
		// TODO get config options here, it can add ips or commands
		// to add routes, run dhcp, rename the interface ... whatever
	}

	// attach the network devices to the pod namespace
	for _, result := range allocation.Devices.Results {
		klog.Infof("StopPodSandbox allocation.Devices.Result: %#v", result)
		err := hostdevice.MoveLinkOut(result.Device, ns)
		if err != nil {
			// Swallow error as deleting the namespace will return the interface to the root namespace anyway
			klog.V(2).Infof("StopPodSandbox pod %s/%s failed to deallocate interface", pod.Namespace, pod.Name)
			return nil
		}
		rdmaDev, err := rdmamap.GetRdmaDeviceForNetdevice(result.Device)
		if err != nil {
			klog.Infof("RunPodSandbox error getting RDMA device %s to namespace %s: %v", result.Device, ns, err)
			continue
		}
		if rdmaDev != "" {
			err = hostdevice.MoveRDMALinkIn(rdmaDev, ns)
			if err != nil {
				klog.Infof("RunPodSandbox error getting RDMA device %s to namespace %s: %v", result.Device, ns, err)
				continue
			}
		}
	}
	return nil
}

//  {
//    "accessConfigs": [
//      {
//       "externalIp": "",
//     "type": "ONE_TO_ONE_NAT"
//    }
//  ],
//  "dnsServers": [
//    "169.254.169.254"
//  ],
//   "forwardedIps": [],
//   "gateway": "192.168.4.1",
//  "ip": "192.168.4.2",
//   "ipAliases": [],
//   "mac": "42:01:c0:a8:04:02",
//   "mtu": 8244,
//   "network": "projects/628944397724/networks/aojea-dra-net-4",
//   "subnetmask": "255.255.255.0",
//   "targetInstanceIps": []
// }

type gceNetworkInterface struct {
	IPv4    string   `json:"ip,omitempty"`
	IPv6    []string `json:"ipv6,omitempty"`
	Mac     string   `json:"mac,omitempty"`
	MTU     int      `json:"mtu,omitempty"`
	Network string   `json:"network,omitempty"`
}

func (np *NetworkPlugin) PublishResources(ctx context.Context) {
	klog.V(2).Infof("Publishing resources")
	// Get google compute instance metadata for network interfaces
	// https://cloud.google.com/compute/docs/metadata/predefined-metadata-keys

	var gceInterfaces []gceNetworkInterface

	if metadata.OnGCE() {
		instanceName, err := metadata.InstanceNameWithContext(ctx)
		if err != nil {
			klog.Infof("could not get instance name on GCE .... skipping GCE network interface attributes: %v", err)
		} else {
			klog.Infof("Getting GCE network interface attributes for instance %s", instanceName)
		}

		// TODO Check accelerator type machines
		instanceType, err := metadata.GetWithContext(ctx, "instance/machine-type")
		if err != nil {
			klog.Infof("could not get instance type on GCE .... skipping GCE network interface attributes: %v", err)
		} else {
			klog.Infof("Getting GCE accelerator attributes for instance type %s", instanceType)
		}

		//  curl "http://metadata.google.internal/computeMetadata/v1/instance/network-interfaces/?recursive=true" -H "Metadata-Flavor: Google"
		// [{"accessConfigs":[{"externalIp":"35.225.164.134","type":"ONE_TO_ONE_NAT"}],"dnsServers":["169.254.169.254"],"forwardedIps":[],"gateway":"10.128.0.1","ip":"10.128.0.70","ipAliases":["10.24.3.0/24"],"mac":"42:01:0a:80:00:46","mtu":1460,"network":"projects/628944397724/networks/default","subnetmask":"255.255.240.0","targetInstanceIps":[]},{"accessConfigs":[{"externalIp":"","type":"ONE_TO_ONE_NAT"}],"dnsServers":["169.254.169.254"],"forwardedIps":[],"gateway":"192.168.1.1","ip":"192.168.1.2","ipAliases":[],"mac":"42:01:c0:a8:01:02","mtu":8244,"network":"projects/628944397724/networks/aojea-dra-net-1","subnetmask":"255.255.255.0","targetInstanceIps":[]},{"accessConfigs":[{"externalIp":"","type":"ONE_TO_ONE_NAT"}],"dnsServers":["169.254.169.254"],"forwardedIps":[],"gateway":"192.168.2.1","ip":"192.168.2.2","ipAliases":[],"mac":"42:01:c0:a8:02:02","mtu":8244,"network":"projects/628944397724/networks/aojea-dra-net-2","subnetmask":"255.255.255.0","targetInstanceIps":[]},{"accessConfigs":[{"externalIp":"","type":"ONE_TO_ONE_NAT"}],"dnsServers":["169.254.169.254"],"forwardedIps":[],"gateway":"192.168.3.1","ip":"192.168.3.2","ipAliases":[],"mac":"42:01:c0:a8:03:02","mtu":8244,"network":"projects/628944397724/networks/aojea-dra-net-3","subnetmask":"255.255.255.0","targetInstanceIps":[]},{"accessConfigs":[{"externalIp":"","type":"ONE_TO_ONE_NAT"}],"dnsServers":["169.254.169.254"],"forwardedIps":[],"gateway":"192.168.4.1","ip":"192.168.4.2","ipAliases":[],"mac":"42:01:c0:a8:04:02","mtu":8244,"network":"projects/628944397724/networks/aojea-dra-net-4","subnetmask":"255.255.255.0","targetInstanceIps":[]}]
		gceInterfacesRaw, err := metadata.GetWithContext(ctx, "instance/network-interfaces/?recursive=true&alt=json")
		if err != nil {
			klog.Infof("could not get network interfaces on GCE .... skipping GCE network interface attributes: %v", err)
		} else {
			klog.Infof("Getting GCE accelerator attributes for instance type %s", instanceType)
			if err = json.Unmarshal([]byte(gceInterfacesRaw), &gceInterfaces); err != nil {
				klog.Infof("could not get network interfaces on GCE .... skipping GCE network interface attributes: %v", err)
			}
		}

	}

	// Resources are published periodically or if there is a netlink notification
	// indicating a new interfaces was added or changed
	nlChannel := make(chan netlink.LinkUpdate)
	doneCh := make(chan struct{})
	defer close(doneCh)
	if err := netlink.LinkSubscribe(nlChannel, doneCh); err != nil {
		klog.Infof("error subscring to netlink interfaces: %v", err)
	}
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		ifaces, err := net.Interfaces()
		if err != nil {
			klog.Infof("error getting system interfaces: %v", err)
		}
		resources := kubeletplugin.Resources{}
		for _, iface := range ifaces {
			klog.V(7).Infof("Checking iface %s", iface.Name)
			// skip default interface
			if iface.Name == np.ifaceGw {
				continue
			}
			// only interested in interfaces that match the regex
			if len(validation.IsDNS1123Label(iface.Name)) > 0 {
				klog.V(2).Infof("iface %s does not pass validation", iface.Name)
				continue
			}
			// skip loopback interface
			if iface.Flags&net.FlagLoopback == net.FlagLoopback {
				continue
			}
			// publish this network interface
			device := resourceapi.Device{
				Name: iface.Name,
				Basic: &resourceapi.BasicDevice{
					Attributes: make(map[resourceapi.QualifiedName]resourceapi.DeviceAttribute),
					Capacity:   make(map[resourceapi.QualifiedName]resource.Quantity),
				},
			}
			device.Basic.Attributes["name"] = resourceapi.DeviceAttribute{StringValue: &iface.Name}

			link, err := netlink.LinkByName(iface.Name)
			if err != nil {
				klog.Infof("Error getting link by name %v", err)
				continue
			}

			switch link := link.(type) {
			case *netlink.Veth:
				// TODO improve this heuristic to detect veth associated to Pods
				// link.PeerNamespace maybe
				if link.PeerName == "eth0" {
					continue
				}
				// Skip all veth interfaces
				continue
			default:
			}
			// iface attributes
			linkType := link.Type()
			linkAttrs := link.Attrs()

			// TODO we can get more info from the kernel
			// https://www.kernel.org/doc/Documentation/ABI/testing/sysfs-class-net
			// Ref: https://github.com/canonical/lxd/blob/main/lxd/resources/network.go

			// sriov device plugin has a more detailed and better discovery
			// https://github.com/k8snetworkplumbingwg/sriov-network-device-plugin/blob/ed1c14dd4c313c7dd9fe4730a60358fbeffbfdd4/cmd/sriovdp/manager.go#L243

			if ips, err := iface.Addrs(); err == nil && len(ips) > 0 {
				// TODO assume only one addres by now
				ip := ips[0].String()
				device.Basic.Attributes["ip"] = resourceapi.DeviceAttribute{StringValue: &ip}
				mac := iface.HardwareAddr.String()
				device.Basic.Attributes["mac"] = resourceapi.DeviceAttribute{StringValue: &mac}
				mtu := int64(iface.MTU)
				device.Basic.Attributes["mtu"] = resourceapi.DeviceAttribute{IntValue: &mtu}
			}

			// check if there is GCE metadata associated
			if len(gceInterfaces) > 0 {
				mac := iface.HardwareAddr.String()
				// this is bounded and small number O(N) is ok
				for _, gceIf := range gceInterfaces {
					if gceIf.Mac == mac {
						device.Basic.Attributes["gceNetwork"] = resourceapi.DeviceAttribute{StringValue: &gceIf.Network}
						break
					}
				}
			}

			device.Basic.Attributes["encapsulation"] = resourceapi.DeviceAttribute{StringValue: &linkAttrs.EncapType}
			operState := linkAttrs.OperState.String()
			device.Basic.Attributes["state"] = resourceapi.DeviceAttribute{StringValue: &operState}
			device.Basic.Attributes["alias"] = resourceapi.DeviceAttribute{StringValue: &linkAttrs.Alias}
			device.Basic.Attributes["type"] = resourceapi.DeviceAttribute{StringValue: &linkType}

			isRDMA := rdmamap.IsRDmaDeviceForNetdevice(iface.Name)
			device.Basic.Attributes["rdma"] = resourceapi.DeviceAttribute{BoolValue: &isRDMA}
			// from https://github.com/k8snetworkplumbingwg/sriov-network-device-plugin/blob/ed1c14dd4c313c7dd9fe4730a60358fbeffbfdd4/pkg/netdevice/netDeviceProvider.go#L99
			isSRIOV := sriovTotalVFs(iface.Name) > 0
			device.Basic.Attributes["sriov"] = resourceapi.DeviceAttribute{BoolValue: &isSRIOV}
			if isSRIOV {
				vfs := int64(sriovNumVFs(iface.Name))
				device.Basic.Attributes["sriov_vfs"] = resourceapi.DeviceAttribute{IntValue: &vfs}
			}
			resources.Devices = append(resources.Devices, device)
		}

		klog.V(4).Infof("Found following network interfaces %#v", resources.Devices)
		if len(resources.Devices) > 0 {
			np.draPlugin.PublishResources(ctx, resources)
		}

		select {
		// trigger a reconcile
		case <-nlChannel:
			// poor man rate limited
			time.Sleep(2 * time.Second)
			// drain the channel
			for len(nlChannel) > 0 {
				<-nlChannel
			}
		case <-ticker.C:
		}
	}
}

func (np *NetworkPlugin) NodePrepareResources(ctx context.Context, request *drapb.NodePrepareResourcesRequest) (*drapb.NodePrepareResourcesResponse, error) {
	if request == nil {
		return nil, nil
	}
	resp := &drapb.NodePrepareResourcesResponse{
		Claims: make(map[string]*drapb.NodePrepareResourceResponse),
	}

	for _, claimReq := range request.GetClaims() {
		klog.Infof("NodePrepareResources: Claim Request %#v", claimReq)
		devices, err := np.nodePrepareResource(ctx, claimReq)
		if err != nil {
			resp.Claims[claimReq.UID] = &drapb.NodePrepareResourceResponse{
				Error: err.Error(),
			}
		} else {
			r := &drapb.NodePrepareResourceResponse{}
			for _, device := range devices {
				pbDevice := &drapb.Device{
					PoolName:   device.PoolName,
					DeviceName: device.DeviceName,
				}
				r.Devices = append(r.Devices, pbDevice)
			}
			resp.Claims[claimReq.UID] = r
		}
	}
	return resp, nil

}

func (np *NetworkPlugin) nodePrepareResource(ctx context.Context, claimReq *drapb.Claim) ([]drapb.Device, error) {
	// The plugin must retrieve the claim itself to get it in the version that it understands.
	claim, err := np.kubeClient.ResourceV1alpha3().ResourceClaims(claimReq.Namespace).Get(ctx, claimReq.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("retrieve claim %s/%s: %w", claimReq.Namespace, claimReq.Name, err)
	}
	if claim.Status.Allocation == nil {
		return nil, fmt.Errorf("claim %s/%s not allocated", claimReq.Namespace, claimReq.Name)
	}
	if claim.UID != types.UID(claim.UID) {
		return nil, fmt.Errorf("claim %s/%s got replaced", claimReq.Namespace, claimReq.Name)
	}
	np.claimAllocations.Add(claim.UID, *claim.Status.Allocation)

	for _, reserved := range claim.Status.ReservedFor {
		if reserved.Resource != "pods" || reserved.APIGroup != "" {
			klog.Infof("claim reference unsupported for %#v", reserved)
			continue
		}
		np.podAllocations.Add(reserved.UID, *claim.Status.Allocation)
	}
	var devices []drapb.Device
	for _, result := range claim.Status.Allocation.Devices.Results {
		requestName := result.Request
		for _, config := range claim.Status.Allocation.Devices.Config {
			if config.Opaque == nil ||
				config.Opaque.Driver != np.driverName ||
				len(config.Requests) > 0 && !slices.Contains(config.Requests, requestName) {
				continue
			}
		}
		device := drapb.Device{
			PoolName:   result.Pool,
			DeviceName: result.Device,
		}
		devices = append(devices, device)
	}

	return devices, nil
}

func (np *NetworkPlugin) NodeUnprepareResources(ctx context.Context, request *drapb.NodeUnprepareResourcesRequest) (*drapb.NodeUnprepareResourcesResponse, error) {
	if request == nil {
		return nil, nil
	}
	resp := &drapb.NodeUnprepareResourcesResponse{
		Claims: make(map[string]*drapb.NodeUnprepareResourceResponse),
	}

	for _, claimReq := range request.Claims {
		err := np.nodeUnprepareResource(ctx, claimReq)
		if err != nil {
			klog.Infof("error unpreparing ressources for claim %s/%s : %v", claimReq.Namespace, claimReq.Name, err)
			resp.Claims[claimReq.UID] = &drapb.NodeUnprepareResourceResponse{
				Error: err.Error(),
			}
		} else {
			resp.Claims[claimReq.UID] = &drapb.NodeUnprepareResourceResponse{}
		}
	}
	return resp, nil
}

func (np *NetworkPlugin) nodeUnprepareResource(ctx context.Context, claimReq *drapb.Claim) error {
	allocation, ok := np.claimAllocations.Get(types.UID(claimReq.UID))
	if !ok {
		klog.Infof("claim request does not exist %s/%s %s", claimReq.Namespace, claimReq.Name, claimReq.UID)
		return nil
	}
	defer np.claimAllocations.Remove(types.UID(claimReq.UID))
	klog.Infof("claim %s/%s with allocation %#v", claimReq.Namespace, claimReq.Name, allocation)
	// TODO do unpreparing things
	return nil
}
