package dra

import (
	"context"
	"fmt"
	"net"
	"os"
	"regexp"
	"slices"
	"time"

	"github.com/Mellanox/rdmamap"
	"github.com/aojea/kubernetes-network-driver/pkg/nri"
	"github.com/vishvananda/netlink"

	"github.com/containerd/nri/pkg/stub"

	resourceapi "k8s.io/api/resource/v1alpha3"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/klog/v2"
	drapb "k8s.io/kubelet/pkg/apis/dra/v1alpha4"
)

var _ drapb.NodeServer = &NetworkPlugin{}

type NetworkPlugin struct {
	driverName string
	kubeClient kubernetes.Interface
	draPlugin  kubeletplugin.DRAPlugin
	nriPlugin  *nri.Plugin

	ifaceGw string
	regex   *regexp.Regexp
}

func Start(ctx context.Context, driverName string, kubeClient kubernetes.Interface, nodeName string) (*NetworkPlugin, error) {
	plugin := &NetworkPlugin{
		driverName: driverName,
		kubeClient: kubeClient,
		nriPlugin:  &nri.Plugin{},
	}

	pluginRegistrationPath := "/var/lib/kubelet/plugins_registry/" + driverName + ".sock"
	driverPluginPath := "/var/lib/kubelet/plugins/" + driverName
	driverPluginSocketPath := driverPluginPath + "/plugin.sock"

	err := os.MkdirAll(driverPluginPath, 0750)
	if err != nil {
		return nil, fmt.Errorf("failed to create plugin path %s: %v", driverPluginPath, err)
	}

	ifaceGw, err := getDefaultGwIf()
	if err != nil {
		return nil, fmt.Errorf("failed to get interface for the default route: %v", err)
	}
	plugin.ifaceGw = ifaceGw

	nriOpts := []stub.Option{
		stub.WithPluginName(driverName),
		stub.WithPluginIdx("00"),
	}

	stub, err := stub.New(plugin.nriPlugin, nriOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create plugin stub: %v", err)
	}

	plugin.nriPlugin.Stub = stub

	// cancel the plugin if the nri plugin fails for any reason
	inCtx, cancel := context.WithCancel(ctx)
	go func() {
		defer cancel()
		err = plugin.nriPlugin.Stub.Run(inCtx)
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
	np.nriPlugin.Stub.Stop()
	np.draPlugin.Stop()
}

func (np *NetworkPlugin) PublishResources(ctx context.Context) {
	klog.V(2).Infof("Publishing resources")

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
			klog.V(2).Infof("Checking iface %s", iface.Name)
			// skip default interface
			if iface.Name == np.ifaceGw {
				continue
			}
			// only interested in interfaces that match the regex
			if np.regex != nil && !np.regex.MatchString(iface.Name) {
				continue
			}
			// TODO skip interfaces that are down ???
			if iface.Flags&net.FlagUp == 0 {
				continue
			}
			// skip loopback interface
			if iface.Flags&net.FlagLoopback == net.FlagLoopback {
				continue
			}
			// publish this network interface
			device := resourceapi.Device{
				Name: np.driverName,
				Basic: &resourceapi.BasicDevice{
					Attributes: make(map[resourceapi.QualifiedName]resourceapi.DeviceAttribute),
					Capacity:   make(map[resourceapi.QualifiedName]resource.Quantity),
				},
			}
			device.Basic.Attributes["name"] = resourceapi.DeviceAttribute{StringValue: &iface.Name}

			link, err := netlink.LinkByName(iface.Name)
			if err != nil {
				klog.Warningf("Error getting link by name %v", err)
				continue
			}

			switch link := link.(type) {
			case *netlink.Veth:
				// TODO improve this heuristic to detect veth associated to Pods
				// link.PeerNamespace maybe
				if link.PeerName == "eth0" {
					continue
				}
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

			device.Basic.Attributes["alias"] = resourceapi.DeviceAttribute{StringValue: &linkAttrs.Alias}
			device.Basic.Attributes["type"] = resourceapi.DeviceAttribute{StringValue: &linkType}

			isRDMA := rdmamap.IsRDmaDeviceForNetdevice(iface.Name)
			device.Basic.Attributes["rdma"] = resourceapi.DeviceAttribute{BoolValue: &isRDMA}
			// from https://github.com/k8snetworkplumbingwg/sriov-network-device-plugin/blob/ed1c14dd4c313c7dd9fe4730a60358fbeffbfdd4/pkg/netdevice/netDeviceProvider.go#L99
			isSRIOV := sriovTotalVFs(iface.Name) > 0
			device.Basic.Attributes["sriov"] = resourceapi.DeviceAttribute{BoolValue: &isSRIOV}

			resources.Devices = append(resources.Devices, device)
		}

		klog.V(2).Infof("Found following network interfaces %v", resources.Devices)
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
	return nil
}
