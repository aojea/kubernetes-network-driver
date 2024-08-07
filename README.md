# Kubernetes Network Drivers

## Kubernetes Networking

Kubernetes networking is conceptually very simple, there is an infrastructure that provides connectivity to Virtual Machines/Servers, these are represented as Nodes that have their own lifecycle. Nodes contain different resources “CPU, Memory, GPUs, ..” that can be consumed by Pods (a group of containerized applications).

The Pods network is based on the End-to-end principle, all applications can communicate with each other, pushing the specific network complexity on the application endpoints rather than on the intermediary devices such as gateways and routers. As a result, the network is no longer the bottleneck and the applications are the ones that limit the reliability and scalability of the system. The network features are implemented by the network plugins. Each Pod has one interface and an unique IP per IP family, the interface connects the Pod network namespace to the root namespace, and is typically added by a CNI plugin, though this is an implementation detail of the container runtime.

In order for the applications to discover each other, Kubernetes offers discovery mechanisms based on virtual IP or DNS, defined by the Services API. Services allow to abstract a set of Pods behind a virtual IP and also are a primitive to build more complex discovery or load balancer mechanisms as Ingress or Gateway API or Service Meshes that practically operate at the application level (L7)

## Advanced Networking

Kubernetes has become defacto platform for containerized platforms, this makes it attractive for different ecosystems like AI/ML and HPC or industries like Telco that want to migrate to a more container native experience but require a more sofisticated networking configuration and a better integration with the hardware.

There is a misbilief across the ecosystem and the industry that any network
configuration in Kubernetes is responsability of the Network Plugin (wrongly
refered as the CNI plugin).

Kubernetes has a pluggable architecture allow at least 3 different ways to add
network interfaces and configurations to the Pods, using CNI, CDI Devices and
Device Plugins, NRI Plugins.

Unfortunately, today there is no good way in Kubernetes to support natively
some of these use cases, SRIOV and AI/ML workloads use a combination of Device
Plugins and CNI multiplexing combos with Multus, or Network Plugins use CRDs
to provide these functionalities using the existing CNI hook at the Pod
creation.

DRA Dynamic Resource Allocation, is a new framework in Kubernetes built to
improve Kubernetes relation with the hardware, that can be used to solve the
problem of advanced network configurations.

## Anatomy of a Networking DRA Driver

The networking DRA drivers uses GRPC to communicate with the Kubelet via the [DRA API](https://github.com/kubernetes/kubernetes/tree/master/staging/src/k8s.io/kubelet/pkg/apis/dra/v1alpha4) and the Container Runtime via [NRI](https://github.com/containerd/nri). This architecture facilitates the supportability and reduces the complexity of the solution, it also makes it fully compatible and agnostic of the existing CNI plugins in the cluster.

Networking DRA drivers authors need to define two business logic:
- publishing node resources: discovery the local resources on the node that the driver should announce, and its attributes and capabilities.
- executing on the ResourceClaim: the Network Driver, before the Pod start to be created, will receive a GRPC call from the Kubelet using the DRA API with the details of the request associated to a Pod. Once the Pod network namespaces has been created, the driver will receive a GRPC call from the Container Runtime via NRI to execute the corresponding configuration. A more detailed diagram can be found in:

[![](https://mermaid.ink/img/pako:eNp9UstuwyAQ_JUVp1ZNfoBDpMi-WFXdyLn6gs0mQTXgLtCHovx714nTWoobDgiW2dlhNEfReo1CioDvCV2LuVF7UrZ2wEul6F2yDdLl_pwa7DAul6vVU4nx09Mb5NUacjIfSBJK5toQ9oqwwuATtRgeHi-9pY8InmEw1_naRGUcxAPCtTPrlLF8Y10hgnIaMu92Zj_S3ZAMqpajwvtSrt_gXzDlMBhJS6iS23i95UmN_7pi_wADf1YWEniDdZ6P72VxfpjwMEmxCXPts55VBRy8f5sff981xoMb605ZDL1qGd4jqWi8C_esmiqGG7FTK2eF_eNhRqgi_lbCjI1T6lu4WAiLZJXRHMrj0FwLToXFWkg-atyp1MVa1O7E0CGg22_XChkp4UKkXjPfmGEhd6oLXEVtoqeXS9DPeT_9ABUC_8M?type=png)](https://mermaid.live/edit#pako:eNp9UstuwyAQ_JUVp1ZNfoBDpMi-WFXdyLn6gs0mQTXgLtCHovx714nTWoobDgiW2dlhNEfReo1CioDvCV2LuVF7UrZ2wEul6F2yDdLl_pwa7DAul6vVU4nx09Mb5NUacjIfSBJK5toQ9oqwwuATtRgeHi-9pY8InmEw1_naRGUcxAPCtTPrlLF8Y10hgnIaMu92Zj_S3ZAMqpajwvtSrt_gXzDlMBhJS6iS23i95UmN_7pi_wADf1YWEniDdZ6P72VxfpjwMEmxCXPts55VBRy8f5sff981xoMb605ZDL1qGd4jqWi8C_esmiqGG7FTK2eF_eNhRqgi_lbCjI1T6lu4WAiLZJXRHMrj0FwLToXFWkg-atyp1MVa1O7E0CGg22_XChkp4UKkXjPfmGEhd6oLXEVtoqeXS9DPeT_9ABUC_8M)

## References

- [Dynamic Resource Allocation #306](https://github.com/kubernetes/enhancements/blob/master/keps/sig-node/3063-dynamic-resource-allocation/README.md)
- [Add CDI devices to device plugin API #40](https://github.com/kubernetes/enhancements/issues/409)
- [DRA: structured parameters #438](https://github.com/kubernetes/enhancements/issues/4381)
- [NVIDIA GPU Use-Cases for Dynamic Resource Allocation (DRA)](https://docs.google.com/document/d/1bDO11rEq_Yhpgpk5RN0VwnMLV1_2wNWvtOyM_QoIV_Y/edit?disco=AAABHIxz8AU)
- [Extend PodResources to include resources from Dynamic Resource Allocation (DRA) #3695](https://github.com/kubernetes/enhancements/issues/3695)
- [WG Device Management](https://github.com/kubernetes-sigs/wg-device-management)
- [Kubernetes Network Drivers](https://docs.google.com/presentation/d/1Vdr7BhbYXeWjwmLjGmqnUkvJr_eOUdU0x-JxfXWxUT8/edit?usp=sharing)
