package cmd

import (
	"context"
	"flag"
	"os"
	"os/signal"

	"golang.org/x/sys/unix"
	"k8s.io/klog/v2"

	"github.com/aojea/kubernetes-network-driver/pkg/nri"

	"github.com/containerd/nri/pkg/stub"
)

const (
	// https://kubernetes.io/docs/concepts/extend-kubernetes/compute-storage-net/device-plugins/
	kubeletSocket = "kubelet.sock"
	pluginSocket  = "netdevice.sock"
	pluginName    = "netdevice"
	resourceName  = "networking.dra.k8s.io"
)

func Main() int {
	// pluginPath := path.Join(pluginapi.DevicePluginPath, pluginSocket)

	var (
		pluginName string
		pluginIdx  string
		opts       []stub.Option
		err        error
		verbose    bool
	)

	flag.StringVar(&pluginName, "name", "", "plugin name to register to NRI")
	flag.StringVar(&pluginIdx, "idx", "", "plugin index to register to NRI")
	flag.BoolVar(&verbose, "verbose", false, "enable (more) verbose logging")

	klog.InitFlags(nil)
	flag.Parse()

	klog.Infof("flags: %v", flag.Args())
	// trap Ctrl+C and call cancel on the context
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)

	// Enable signal handler
	signalCh := make(chan os.Signal, 2)
	defer func() {
		close(signalCh)
		cancel()
	}()
	signal.Notify(signalCh, os.Interrupt, unix.SIGINT)

	if pluginName != "" {
		opts = append(opts, stub.WithPluginName(pluginName))
	}
	if pluginIdx != "" {
		opts = append(opts, stub.WithPluginIdx(pluginIdx))
	}

	p := &nri.Plugin{}
	if p.Stub, err = stub.New(p, opts...); err != nil {
		klog.Fatalf("failed to create plugin stub: %v", err)
	}

	err = p.Stub.Run(ctx)
	if err != nil {
		klog.Infof("plugin exited with error %v", err)
		return 1
	}

	select {
	case <-signalCh:
		klog.Infof("Exiting: received signal")
		cancel()
	case <-ctx.Done():
	}

	return 0
}
