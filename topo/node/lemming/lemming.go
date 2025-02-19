// Copyright 2022 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package lemming contains a node implementation for a lemming device.
package lemming

import (
	"context"
	"fmt"
	"io"

	"github.com/openconfig/kne/topo/node"
	"github.com/openconfig/lemming/operator/api/clientset"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/client-go/rest"

	tpb "github.com/openconfig/kne/proto/topo"
	lemmingv1 "github.com/openconfig/lemming/operator/api/lemming/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	log "k8s.io/klog/v2"
)

func New(nodeImpl *node.Impl) (node.Node, error) {
	if nodeImpl == nil {
		return nil, fmt.Errorf("nodeImpl cannot be nil")
	}
	if nodeImpl.Proto == nil {
		return nil, fmt.Errorf("nodeImpl.Proto cannot be nil")
	}
	cfg := defaults(nodeImpl.Proto)
	nodeImpl.Proto = cfg
	n := &Node{
		Impl: nodeImpl,
	}
	return n, nil
}

type Node struct {
	*node.Impl
}

// Add validations for interfaces the node provides
var (
	_ node.Certer       = (*Node)(nil)
	_ node.ConfigPusher = (*Node)(nil)
	_ node.Resetter     = (*Node)(nil)
)

var clientFn = func(c *rest.Config) (clientset.Interface, error) {
	return clientset.NewForConfig(c)
}

func (n *Node) Create(ctx context.Context) error {
	nodeSpec := n.GetProto()
	config := nodeSpec.GetConfig()
	log.Infof("create lemming %q", nodeSpec.Name)

	ports := map[string]lemmingv1.ServicePort{}

	for k, v := range n.Proto.Services {
		ports[v.Name] = lemmingv1.ServicePort{
			InnerPort: int32(v.Inside),
			OuterPort: int32(k),
		}
	}

	dut := &lemmingv1.Lemming{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nodeSpec.Name,
			Namespace: n.Namespace,
			Labels:    nodeSpec.Labels,
		},
		Spec: lemmingv1.LemmingSpec{
			Image:          config.Image,
			Command:        config.Command[0],
			Args:           config.Args,
			Env:            node.ToEnvVar(config.Env),
			ConfigPath:     config.ConfigPath,
			ConfigFile:     config.ConfigFile,
			InitImage:      config.InitImage,
			Ports:          ports,
			InterfaceCount: len(nodeSpec.Interfaces) + 1,
			InitSleep:      int(config.Sleep),
			Resources:      node.ToResourceRequirements(nodeSpec.Constraints),
		},
	}
	if config.Cert != nil {
		switch tls := config.Cert.Config.(type) {
		case *tpb.CertificateCfg_SelfSigned:
			dut.Spec.TLS = &lemmingv1.TLSSpec{
				SelfSigned: &lemmingv1.SelfSignedSpec{
					CommonName: tls.SelfSigned.CommonName,
					KeySize:    int(tls.SelfSigned.KeySize),
				},
			}
		}
	}

	cs, err := clientFn(n.RestConfig)
	if err != nil {
		return fmt.Errorf("failed to get kubernetes client: %v", err)
	}
	if _, err := cs.LemmingV1alpha1().Lemmings(n.Namespace).Create(ctx, dut, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("failed to create lemming: %v", err)
	}
	return nil
}

func (n *Node) Status(ctx context.Context) (node.Status, error) {
	cs, err := clientFn(n.RestConfig)
	if err != nil {
		return node.StatusUnknown, err
	}
	got, err := cs.LemmingV1alpha1().Lemmings(n.Namespace).Get(ctx, n.Name(), metav1.GetOptions{})
	if err != nil {
		return node.StatusUnknown, err
	}
	switch got.Status.Phase {
	case lemmingv1.Running:
		return node.StatusRunning, nil
	case lemmingv1.Failed:
		return node.StatusFailed, nil
	case lemmingv1.Pending:
		return node.StatusPending, nil
	default:
		return node.StatusUnknown, nil
	}
}

func (n *Node) Delete(ctx context.Context) error {
	cs, err := clientFn(n.RestConfig)
	if err != nil {
		return err
	}
	return cs.LemmingV1alpha1().Lemmings(n.Namespace).Delete(ctx, n.Name(), metav1.DeleteOptions{})
}

func (n *Node) ResetCfg(ctx context.Context) error {
	log.Info("ResetCfg is a noop.")
	return nil
}

func (n *Node) ConfigPush(context.Context, io.Reader) error {
	return status.Errorf(codes.Unimplemented, "config push is not implemented using gNMI to configure device")
}

func (n *Node) GenerateSelfSigned(context.Context) error {
	return status.Errorf(codes.Unimplemented, "certificate generation is not supported")
}

func defaults(pb *tpb.Node) *tpb.Node {
	if pb.Config == nil {
		pb.Config = &tpb.Config{}
	}
	if pb.Config.Image == "" {
		pb.Config.Image = "us-west1-docker.pkg.dev/openconfig-lemming/release/lemming:ga"
	}
	if pb.Config.InitImage == "" {
		pb.Config.InitImage = node.DefaultInitContainerImage
	}
	if len(pb.GetConfig().GetCommand()) == 0 {
		pb.Config.Command = []string{"/lemming/lemming"}
	}
	if pb.Config.EntryCommand == "" {
		pb.Config.EntryCommand = fmt.Sprintf("kubectl exec -it %s -- /bin/bash", pb.Name)
	}
	if pb.Config.Cert == nil {
		pb.Config.Cert = &tpb.CertificateCfg{
			Config: &tpb.CertificateCfg_SelfSigned{
				SelfSigned: &tpb.SelfSignedCertCfg{
					CommonName: pb.Name,
					KeySize:    2048,
				},
			},
		}
	}
	if pb.Constraints == nil {
		pb.Constraints = map[string]string{
			"cpu":    "0.5",
			"memory": "1Gi",
		}
	}
	if pb.Labels == nil {
		pb.Labels = map[string]string{
			"vendor": tpb.Vendor_OPENCONFIG.String(),
		}
	}
	if pb.Services == nil {
		pb.Services = map[uint32]*tpb.Service{
			// https://www.iana.org/assignments/service-names-port-numbers/service-names-port-numbers.xhtml?search=gnmi
			9339: {
				Name:   "gnmi",
				Inside: 9339,
			},
			9340: {
				Name:   "gribi",
				Inside: 9340,
			},
			9341: {
				Name:   "gnsi",
				Inside: 9339,
			},
			9342: {
				Name:   "gnoi",
				Inside: 9339,
			},
		}
	}
	return pb
}

func init() {
	node.Vendor(tpb.Vendor_OPENCONFIG, New)
}
