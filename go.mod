module github.com/aojea/kubernetes-network-driver

go 1.22.0

require (
	cloud.google.com/go/compute/metadata v0.5.0
	github.com/Mellanox/rdmamap v1.1.0
	github.com/containerd/nri v0.6.1
	github.com/containernetworking/plugins v1.5.1
	github.com/vishvananda/netlink v1.2.1-beta.2
	golang.org/x/sys v0.22.0
	k8s.io/api v0.30.3
	k8s.io/apimachinery v0.30.3
	k8s.io/client-go v0.30.3
	k8s.io/component-helpers v0.30.3
	k8s.io/dynamic-resource-allocation v0.0.0-00010101000000-000000000000
	k8s.io/klog/v2 v2.130.1
	k8s.io/kubelet v0.0.0
)

require (
	github.com/containerd/ttrpc v1.2.3 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/emicklei/go-restful/v3 v3.11.0 // indirect
	github.com/fxamacker/cbor/v2 v2.7.0 // indirect
	github.com/go-logr/logr v1.4.2 // indirect
	github.com/go-openapi/jsonpointer v0.19.6 // indirect
	github.com/go-openapi/jsonreference v0.20.2 // indirect
	github.com/go-openapi/swag v0.22.4 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/gnostic-models v0.6.8 // indirect
	github.com/google/go-cmp v0.6.0 // indirect
	github.com/google/gofuzz v1.2.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/imdario/mergo v0.3.6 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/opencontainers/runtime-spec v1.0.3-0.20220825212826-86290f6a00fb // indirect
	github.com/sirupsen/logrus v1.9.3 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	github.com/vishvananda/netns v0.0.4 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	golang.org/x/net v0.26.0 // indirect
	golang.org/x/oauth2 v0.21.0 // indirect
	golang.org/x/term v0.21.0 // indirect
	golang.org/x/text v0.16.0 // indirect
	golang.org/x/time v0.3.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240701130421-f6361c86f094 // indirect
	google.golang.org/grpc v1.65.0 // indirect
	google.golang.org/protobuf v1.34.2 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	k8s.io/cri-api v0.25.3 // indirect
	k8s.io/kube-openapi v0.0.0-20240228011516-70dd3763d340 // indirect
	k8s.io/utils v0.0.0-20240711033017-18e509b52bc8 // indirect
	sigs.k8s.io/json v0.0.0-20221116044647-bc3834ca7abd // indirect
	sigs.k8s.io/structured-merge-diff/v4 v4.4.1 // indirect
	sigs.k8s.io/yaml v1.4.0 // indirect
)

replace (
	k8s.io/api => github.com/kubernetes/kubernetes/staging/src/k8s.io/api v0.0.0-20240724042040-57d197fb890a
	k8s.io/apimachinery => github.com/kubernetes/kubernetes/staging/src/k8s.io/apimachinery v0.0.0-20240724042040-57d197fb890a
	k8s.io/client-go => github.com/kubernetes/kubernetes/staging/src/k8s.io/client-go v0.0.0-20240724042040-57d197fb890a
	k8s.io/component-base => github.com/kubernetes/kubernetes/staging/src/k8s.io/component-base v0.0.0-20240724042040-57d197fb890a
	k8s.io/dynamic-resource-allocation => github.com/kubernetes/kubernetes/staging/src/k8s.io/dynamic-resource-allocation v0.0.0-20240724042040-57d197fb890a
	k8s.io/kubelet => github.com/kubernetes/kubernetes/staging/src/k8s.io/kubelet v0.0.0-20240724042040-57d197fb890a
)
