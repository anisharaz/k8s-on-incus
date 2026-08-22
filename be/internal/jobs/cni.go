package jobs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/anisharaz/incus-k8s-manager/be/internal/models"
)

// cniInstallers maps a validated CNI value (see handlers.validateCNI) to its
// install routine. Adding a new CNI later means adding one function below
// and one entry here — runNodeJob's control flow never changes (except for
// kubeadmInitExtraArgs below, for a CNI whose manifest needs a specific
// `kubeadm init` flag).
var cniInstallers = map[string]func(ctx context.Context, m *Manager, incusName string) error{
	string(models.CNITypeCilium):        installCilium,
	string(models.CNITypeCalico):        installCalico,
	string(models.CNITypeFlannel):       installFlannel,
	string(models.CNITypeOVNKubernetes): installOVNKubernetes,
}

// installCNI installs cni onto the master node's cluster. cni must already
// be validated by the caller — an unrecognized value here is a programming
// error, not user input.
func installCNI(ctx context.Context, m *Manager, incusName, cni string) error {
	installer, ok := cniInstallers[cni]
	if !ok {
		return fmt.Errorf("no installer registered for cni %q", cni)
	}
	return installer(ctx, m, incusName)
}

// kubeadmInitExtraArgs are additional flags runNodeJob must pass to
// `kubeadm init` for a given CNI, beyond the plain, CNI-agnostic
// invocation. Cilium manages its own IPAM without needing kubeadm to
// allocate a per-node pod CIDR, so it gets no entry here — a missing map
// key means "no extra args" (see kubeadmInitArgs).
//
// Flannel's stock manifest hardcodes pod CIDR 10.244.0.0/16, which only
// works if kube-controller-manager was told to allocate that same CIDR to
// each node — which kubeadm only does when --pod-network-cidr is passed at
// init time.
//
// Calico's operator-based install (installCalico) also needs this,
// contrary to Tigera's own docs suggesting otherwise: the operator reads
// kubeadm's own kubeadm-config ConfigMap to auto-detect the pod CIDR, and
// if `--pod-network-cidr` was never passed to `kubeadm init`, that
// ConfigMap has no podSubnet field at all — confirmed live, the
// "ippools" TigeraStatus component gets permanently stuck with "error
// filling IP pool defaults: kubeadm configuration is missing required
// podSubnet field" rather than falling back to any default. Using
// 192.168.0.0/16 here — Calico's own documented default IP pool CIDR — so
// nothing else about the stock custom-resources.yaml manifest needs
// customizing to match.
//
// OVN-Kubernetes needs three flags: the same style of --pod-network-cidr
// (its chart defaults expect it too, using the same 10.244.0.0/16 as
// Flannel — the two never coexist since only one CNI installs per
// cluster), an explicit --service-cidr (kubeadm's own default, kept
// explicit to match the chart's defaults rather than relying on kubeadm's
// default staying the same), and --skip-phases=addon/kube-proxy since
// OVN-Kubernetes replaces kube-proxy's functionality itself. Confirmed
// live this is sufficient on its own — kube-proxy is never created, so
// installOVNKubernetes doesn't need to also delete it.
var kubeadmInitExtraArgs = map[string][]string{
	string(models.CNITypeCalico):        {"--pod-network-cidr=192.168.0.0/16"},
	string(models.CNITypeFlannel):       {"--pod-network-cidr=10.244.0.0/16"},
	string(models.CNITypeOVNKubernetes): {"--pod-network-cidr=10.244.0.0/16", "--service-cidr=10.96.0.0/16", "--skip-phases=addon/kube-proxy"},
}

// kubeadmInitArgs returns the full `kubeadm init` argv for cni, including
// the binary name, so callers can pass the result straight to
// incus.Client.Run.
func kubeadmInitArgs(cni string) []string {
	return append([]string{"kubeadm", "init"}, kubeadmInitExtraArgs[cni]...)
}

// ciliumCLIInstallScript follows docs.cilium.io's Linux Cilium CLI install
// steps, minus sudo (already root inside the VM).
const ciliumCLIInstallScript = `set -euo pipefail
CILIUM_CLI_VERSION=$(curl -sL https://raw.githubusercontent.com/cilium/cilium-cli/main/stable.txt)
CLI_ARCH=amd64
if [ "$(uname -m)" = "aarch64" ]; then CLI_ARCH=arm64; fi
cd /tmp
curl -L --fail --remote-name-all "https://github.com/cilium/cilium-cli/releases/download/${CILIUM_CLI_VERSION}/cilium-linux-${CLI_ARCH}.tar.gz"{,.sha256sum}
sha256sum --check "cilium-linux-${CLI_ARCH}.tar.gz.sha256sum"
tar xzvfC "cilium-linux-${CLI_ARCH}.tar.gz" /usr/local/bin
rm -f "cilium-linux-${CLI_ARCH}.tar.gz"{,.sha256sum}
`

// installCilium installs the Cilium CLI on incusName, runs `cilium install`
// against the root kubeconfig runNodeJob already copied there, and blocks
// on `cilium status --wait` until Cilium reports healthy or times out.
func installCilium(ctx context.Context, m *Manager, incusName string) error {
	if _, err := m.incus.Run(ctx, incusName, []string{"bash", "-c", ciliumCLIInstallScript}); err != nil {
		return fmt.Errorf("installing cilium CLI: %w", err)
	}

	installCmd := []string{"cilium", "install", "--kubeconfig=" + rootKubeconfigPath}
	if _, err := m.incus.Run(ctx, incusName, installCmd); err != nil {
		return fmt.Errorf("cilium install: %w", err)
	}

	statusCmd := []string{"cilium", "status", "--wait", "--kubeconfig=" + rootKubeconfigPath, "--wait-duration=8m"}
	if _, err := m.incus.Run(ctx, incusName, statusCmd); err != nil {
		return fmt.Errorf("cilium status --wait: %w", err)
	}

	return nil
}

// calicoVersion pins the manifest set fetched below. Unlike Cilium (which
// resolves its CLI version live via stable.txt) and Flannel (which always
// fetches the latest release asset), Calico's own quickstart docs pin an
// explicit tag in their example commands — there's no "always latest" URL
// for the operator manifests, so this needs bumping by hand periodically.
const calicoVersion = "v3.32.1"

var (
	calicoCRDManifestURL      = "https://raw.githubusercontent.com/projectcalico/calico/" + calicoVersion + "/manifests/v1_crd_projectcalico_org.yaml"
	calicoOperatorManifestURL = "https://raw.githubusercontent.com/projectcalico/calico/" + calicoVersion + "/manifests/tigera-operator.yaml"
	calicoCustomResourcesURL  = "https://raw.githubusercontent.com/projectcalico/calico/" + calicoVersion + "/manifests/custom-resources.yaml"
)

// installCalico follows Tigera's documented kubeadm/on-prem install order:
// the standalone CRD bundle, then the operator, then the Installation
// custom resource the operator reconciles against. `kubectl create` (not
// apply) is upstream's own guidance — `apply`'s three-way-merge annotation
// can exceed request size limits against a CRD bundle this large.
func installCalico(ctx context.Context, m *Manager, incusName string) error {
	steps := []struct {
		label string
		url   string
	}{
		{"calico CRDs", calicoCRDManifestURL},
		{"tigera operator", calicoOperatorManifestURL},
		{"calico custom resources", calicoCustomResourcesURL},
	}
	for _, step := range steps {
		cmd := []string{"kubectl", "--kubeconfig=" + rootKubeconfigPath, "create", "-f", step.url}
		if _, err := m.incus.Run(ctx, incusName, cmd); err != nil {
			return fmt.Errorf("applying %s: %w", step.label, err)
		}
	}
	return waitForCalicoReady(ctx, m, incusName)
}

// waitForCalicoReady polls the operator-managed TigeraStatus object named
// "calico" until its Available condition is True. A poll loop (matching
// waitForClusterHealthy/waitForNodeRegistered's existing idiom elsewhere in
// this package) rather than a single `kubectl wait` is deliberate: the
// operator creates this object asynchronously after custom-resources.yaml
// is applied, so a `kubectl wait` issued immediately can race a "not
// found" error — treating any non-zero exit (including "not found") as
// "not ready yet" sidesteps that entirely.
func waitForCalicoReady(ctx context.Context, m *Manager, incusName string) error {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	checkCmd := []string{
		"kubectl", "--kubeconfig=" + rootKubeconfigPath, "get", "tigerastatus", "calico",
		"-o", `jsonpath={.status.conditions[?(@.type=="Available")].status}`,
	}

	for {
		result, err := m.incus.Exec(ctx, incusName, checkCmd, nil)
		if err == nil && result.ExitCode == 0 && strings.TrimSpace(result.Stdout) == "True" {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("calico did not become available on instance %q: %w", incusName, ctx.Err())
		case <-ticker.C:
		}
	}
}

// flannelManifestURL always resolves to the latest GitHub release asset —
// mirrors Cilium's "resolve version live" convention rather than pinning.
const flannelManifestURL = "https://github.com/flannel-io/flannel/releases/latest/download/kube-flannel.yml"

// installFlannel applies Flannel's manifest (kubectl fetches the URL
// itself — no separate curl/tmpfile step needed, unlike Cilium's CLI
// binary) and blocks on its DaemonSet rollout. Requires kubeadmInitArgs to
// have passed --pod-network-cidr at `kubeadm init` time — see the comment
// on kubeadmInitExtraArgs.
func installFlannel(ctx context.Context, m *Manager, incusName string) error {
	applyCmd := []string{"kubectl", "--kubeconfig=" + rootKubeconfigPath, "apply", "-f", flannelManifestURL}
	if _, err := m.incus.Run(ctx, incusName, applyCmd); err != nil {
		return fmt.Errorf("applying flannel manifest: %w", err)
	}

	rolloutCmd := []string{
		"kubectl", "--kubeconfig=" + rootKubeconfigPath, "rollout", "status",
		"daemonset/kube-flannel-ds", "-n", "kube-flannel", "--timeout=5m",
	}
	if _, err := m.incus.Run(ctx, incusName, rolloutCmd); err != nil {
		return fmt.Errorf("waiting for flannel rollout: %w", err)
	}
	return nil
}

// ovnKubernetesCloneDir is where the chart source is sparse-cloned to —
// there's no published Helm repo or OCI chart for ovn-kubernetes, only the
// source repo, and a naive full clone is ~160MB for a ~1MB chart directory.
const ovnKubernetesCloneDir = "/tmp/ovn-kubernetes"

// ovnKubernetesImageTag: upstream publishes no stable/versioned tag for
// this image, only a floating "master" tag — a real reproducibility gap
// (unlike Calico's pinned version or Flannel's "latest release" semantics)
// worth being upfront about rather than silently accepting.
const ovnKubernetesImageTag = "master"

const ovnKubernetesImageRepo = "ghcr.io/ovn-kubernetes/ovn-kubernetes/ovn-kube-ubuntu"

// ovnKubernetesInstallScript prepares the host for the Helm install below.
// git and openvswitch-switch are baked into the VM image itself (see
// meta/incusDocker/incusStuff/incus_distrobuilder.yaml) rather than
// apt-installed here, since they rarely change and apt-get at
// cluster-creation time is exactly the kind of live-network fragility this
// codebase otherwise avoids (see the VM image's kubeadm-images pre-pull for
// the same reasoning). Helm itself is curl-installed live, matching
// Cilium's own "curl a small tool binary at cluster time" pattern — unlike
// git/openvswitch-switch, its own install script already tracks upstream
// releases without needing this codebase to pin anything.
// A pre-installer version of this script used to run `ovs-vsctl add-br
// br-int` here to pre-create the integration bridge. That was removed:
// confirmed live it's actively harmful now that the host OVS
// daemons are masked (see incus_distrobuilder.yaml) — `ovs-vsctl` hangs
// forever with no ovsdb-server to talk to. It was also never useful even
// when the host daemons WERE running: it writes to the host package's own
// database (/etc/openvswitch), while the ovs-node DaemonSet's containerized
// OVS daemon uses a completely separate one (/etc/origin/openvswitch) and
// creates br-int itself — confirmed live, ovs-node's own logs show it
// creating br-int/br-ex on its own once the host daemons are out of its way.
const ovnKubernetesInstallScript = `set -euo pipefail
curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
rm -rf ` + ovnKubernetesCloneDir + `
git clone --depth 1 --filter=blob:none --sparse https://github.com/ovn-kubernetes/ovn-kubernetes.git ` + ovnKubernetesCloneDir + `
cd ` + ovnKubernetesCloneDir + `
git sparse-checkout set helm/ovn-kubernetes
`

// installOVNKubernetes is a deliberately degraded but safe install:
// global.dummyGatewayBridge=true avoids binding the VM's real NIC into an
// OVS bridge — the alternative (the chart's default gateway modes) would
// mean reconfiguring the exact interface Incus's IP detection, the guest
// agent, and every m.incus.Run call in this job depend on, with no
// rollback path if it goes wrong mid-VM. The tradeoff: pod-to-pod
// networking works, external/NodePort access doesn't. This is why the CNI
// is offered as "experimental" in the UI (see allowedCNIs' comment) —
// it's a real, working, safely-installed CNI, just a materially reduced
// one compared to Cilium/Calico/Flannel.
//
// kube-proxy is never installed in the first place, via
// --skip-phases=addon/kube-proxy at kubeadm init (kubeadmInitExtraArgs) —
// confirmed live no explicit removal is needed here.
func installOVNKubernetes(ctx context.Context, m *Manager, incusName string) error {
	if _, err := m.incus.Run(ctx, incusName, []string{"bash", "-c", ovnKubernetesInstallScript}); err != nil {
		return fmt.Errorf("installing ovn-kubernetes prerequisites: %w", err)
	}

	// k8sAPIServer must be the address kubeadm actually put in the
	// apiserver's serving certificate SANs (the node's real IP plus the
	// service cluster IP) — confirmed live that kubeadm's default cert does
	// NOT include 127.0.0.1/localhost. Hardcoding 127.0.0.1 here previously
	// meant every ovn-kubernetes component dialing the apiserver got a TLS
	// verification error ("certificate is valid for 10.96.0.1, <node-ip>,
	// not 127.0.0.1"), which for ovnkube-identity specifically meant its
	// node informer cache never synced, which in turn meant its admission
	// webhook HTTP server never started listening on :9443 at all — every
	// kubelet node-status PATCH then failed webhook validation with
	// "connection refused", leaving the node stuck NotReady forever even
	// though the individual pods were actually running. Reading the address
	// straight out of the kubeconfig kubeadm already generated avoids
	// hardcoding it a second time somewhere it can drift out of sync.
	apiServerCmd := []string{
		"kubectl", "--kubeconfig=" + rootKubeconfigPath, "config", "view",
		"--minify", "-o", "jsonpath={.clusters[0].cluster.server}",
	}
	apiServerResult, err := m.incus.Exec(ctx, incusName, apiServerCmd, nil)
	if err != nil {
		return fmt.Errorf("reading kube-apiserver address from kubeconfig: %w", err)
	}
	if apiServerResult.ExitCode != 0 {
		return fmt.Errorf("reading kube-apiserver address from kubeconfig: exit %d: %s", apiServerResult.ExitCode, apiServerResult.Stdout)
	}
	k8sAPIServer := strings.TrimSpace(apiServerResult.Stdout)

	helmInstallCmd := []string{
		"helm", "install", "ovn-kubernetes",
		ovnKubernetesCloneDir + "/helm/ovn-kubernetes",
		"-f", ovnKubernetesCloneDir + "/helm/ovn-kubernetes/values-single-node-zone.yaml",
		"--set", "k8sAPIServer=" + k8sAPIServer,
		"--set", "global.image.repository=" + ovnKubernetesImageRepo,
		"--set", "global.image.tag=" + ovnKubernetesImageTag,
		"--set", "global.dummyGatewayBridge=true",
		"--kubeconfig=" + rootKubeconfigPath,
	}
	if _, err := m.incus.Run(ctx, incusName, helmInstallCmd); err != nil {
		return fmt.Errorf("helm install ovn-kubernetes: %w", err)
	}

	rolloutCmd := []string{
		"kubectl", "--kubeconfig=" + rootKubeconfigPath, "rollout", "status",
		"daemonset/ovnkube-node", "-n", "ovn-kubernetes", "--timeout=10m",
	}
	if _, err := m.incus.Run(ctx, incusName, rolloutCmd); err != nil {
		return fmt.Errorf("waiting for ovn-kubernetes rollout: %w", err)
	}
	return nil
}
