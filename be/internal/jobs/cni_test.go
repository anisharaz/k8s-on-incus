package jobs

import (
	"reflect"
	"testing"

	"github.com/anisharaz/incus-k8s-manager/be/internal/models"
)

func TestKubeadmInitArgs(t *testing.T) {
	cases := []struct {
		cni  string
		want []string
	}{
		{string(models.CNITypeCilium), []string{"kubeadm", "init"}},
		{string(models.CNITypeCalico), []string{"kubeadm", "init", "--pod-network-cidr=192.168.0.0/16"}},
		{string(models.CNITypeFlannel), []string{"kubeadm", "init", "--pod-network-cidr=10.244.0.0/16"}},
		{
			string(models.CNITypeOVNKubernetes),
			[]string{
				"kubeadm", "init",
				"--pod-network-cidr=10.244.0.0/16",
				"--service-cidr=10.96.0.0/16",
				"--skip-phases=addon/kube-proxy",
			},
		},
		{"unknown-cni", []string{"kubeadm", "init"}},
	}

	for _, tc := range cases {
		got := kubeadmInitArgs(tc.cni)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("kubeadmInitArgs(%q) = %v, want %v", tc.cni, got, tc.want)
		}
	}
}

func TestCNIInstallersRegisteredForEveryAllowedType(t *testing.T) {
	for _, cni := range []models.CNIType{
		models.CNITypeCilium,
		models.CNITypeCalico,
		models.CNITypeFlannel,
		models.CNITypeOVNKubernetes,
	} {
		if _, ok := cniInstallers[string(cni)]; !ok {
			t.Errorf("no installer registered for cni %q", cni)
		}
	}
}
