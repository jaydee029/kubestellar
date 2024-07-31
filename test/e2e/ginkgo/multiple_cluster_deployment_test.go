/*
Copyright 2023 The KubeStellar Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	ksapi "github.com/kubestellar/kubestellar/api/control/v1alpha1"
	"github.com/kubestellar/kubestellar/test/util"
)

const (
	ns = "nginx"
)

var _ = ginkgo.Describe("end to end testing", func() {
	ginkgo.BeforeEach(func(ctx context.Context) {
		// Cleanup the WDS, create 1 deployment and 1 binding policy.
		util.CleanupWDS(ctx, wds, ksWds, ns)
		util.CreateDeployment(ctx, wds, ns, "nginx",
			map[string]string{
				"app.kubernetes.io/name":         "nginx",
				"test.kubestellar.io/test-label": "here",
			})
		util.CreateBindingPolicy(ctx, ksWds, "nginx",
			[]metav1.LabelSelector{
				{MatchLabels: map[string]string{"location-group": "edge"}},
			},
			[]ksapi.DownsyncPolicyClause{
				{DownsyncObjectTest: ksapi.DownsyncObjectTest{
					ObjectSelectors: []metav1.LabelSelector{{MatchLabels: map[string]string{"app.kubernetes.io/name": "nginx"}}},
				}},
			},
		)
	})

	ginkgo.Context("multiple WECs", func() {
		ginkgo.It("propagates deployment to the WECs while applying CustomTransform", func(ctx context.Context) {
			util.CreateCustomTransform(ctx, ksWds, "test", "apps", "deployments", `$.metadata.labels["test.kubestellar.io/test-label"]`)
			testLabelAbsent := func(deployment *appsv1.Deployment) string {
				if _, has := deployment.Labels["test.kubestellar.io/test-label"]; has {
					return "it has the 'test.kubestellar.io/test-label' label"
				}
				return ""
			}
			util.ValidateNumDeployments(ctx, wec1, ns, 1, testLabelAbsent)
			util.ValidateNumDeployments(ctx, wec2, ns, 1, testLabelAbsent)
		})

		ginkgo.It("updates objects on the WECs following an update on the WDS", func(ctx context.Context) {
			patch := []byte(`{"spec":{"replicas": 2}}`)
			_, err := wds.AppsV1().Deployments(ns).Patch(
				ctx, "nginx", types.MergePatchType, patch, metav1.PatchOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			util.ValidateNumDeploymentReplicas(ctx, wec1, ns, 2)
			util.ValidateNumDeploymentReplicas(ctx, wec2, ns, 2)
		})

		ginkgo.It("supports create-only mode", func(ctx context.Context) {
			ginkgo.By("deleting old BindingPolicy and expecting Deployment deletions")
			util.DeleteBindingPolicy(ctx, ksWds, "nginx")
			util.ValidateNumDeploymentReplicas(ctx, wec1, ns, 0)
			util.ValidateNumDeploymentReplicas(ctx, wec2, ns, 0)

			ginkgo.By("creating new BindingPolicy and expecting corresponding Binding")
			util.CreateBindingPolicy(ctx, ksWds, "nginx",
				[]metav1.LabelSelector{
					{MatchLabels: map[string]string{"location-group": "edge"}},
				},
				[]ksapi.DownsyncPolicyClause{
					{DownsyncObjectTest: ksapi.DownsyncObjectTest{
						Resources:       []string{"namespaces"},
						ObjectSelectors: []metav1.LabelSelector{{MatchLabels: map[string]string{"app.kubernetes.io/name": "nginx"}}},
					}},
					{DownsyncObjectTest: ksapi.DownsyncObjectTest{
						Resources:       []string{"deployments"},
						ObjectSelectors: []metav1.LabelSelector{{MatchLabels: map[string]string{"app.kubernetes.io/name": "nginx"}}},
					},
						CreateOnly: true},
				},
			)
			util.ValidateBinding(ctx, ksWds, "nginx", func(binding *ksapi.Binding) bool {
				klog.FromContext(ctx).V(2).Info("Checking Binding", "binding", binding)
				return len(binding.Spec.Workload.ClusterScope) == 1 &&
					!binding.Spec.Workload.ClusterScope[0].CreateOnly &&
					len(binding.Spec.Workload.NamespaceScope) == 1 &&
					binding.Spec.Workload.NamespaceScope[0].CreateOnly
			})
			util.ValidateNumDeploymentReplicas(ctx, wec1, ns, 1)
			util.ValidateNumDeploymentReplicas(ctx, wec2, ns, 1)

			if false { // TODO: enable this after the transport plugin implements create-only
				ginkgo.By("modifying the Deployment in the WDS and expecting no change in the WECs")
				objPatch := []byte(`{"spec":{"replicas": 2}}`)
				_, err := wds.AppsV1().Deployments(ns).Patch(
					ctx, "nginx", types.MergePatchType, objPatch, metav1.PatchOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				time.Sleep(30 * time.Second)
				util.ValidateNumDeploymentReplicas(ctx, wec1, ns, 1)
				util.ValidateNumDeploymentReplicas(ctx, wec2, ns, 1)
			}
		})

		ginkgo.It("handles changes in bindingpolicy ObjectSelector", func(ctx context.Context) {
			ginkgo.By("deleting WEC objects when bindingpolicy ObjectSelector stops matching")
			patch := []byte(`{"spec": {"downsync": [{"objectSelectors": [{"matchLabels": {"app.kubernetes.io/name": "invalid"}}]}]}}`)
			_, err := ksWds.ControlV1alpha1().BindingPolicies().Patch(
				ctx, "nginx", types.MergePatchType, patch, metav1.PatchOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			util.ValidateNumDeployments(ctx, wec1, ns, 0)
			util.ValidateNumDeployments(ctx, wec2, ns, 0)

			ginkgo.By("creating WEC objects when bindingpolicy ObjectSelector matches")
			patch = []byte(`{"spec": {"downsync": [{"objectSelectors": [{"matchLabels": {"app.kubernetes.io/name": "nginx"}}]}]}}`)
			_, err = ksWds.ControlV1alpha1().BindingPolicies().Patch(
				ctx, "nginx", types.MergePatchType, patch, metav1.PatchOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			util.ValidateNumDeployments(ctx, wec1, ns, 1)
			util.ValidateNumDeployments(ctx, wec2, ns, 1)
		})

		ginkgo.It("handles changes in workload object labels", func(ctx context.Context) {
			ginkgo.By("deleting WEC objects when workload object labels stop matching")
			patch := []byte(`{"metadata": {"labels": {"app.kubernetes.io/name": "not-me"}}}`)
			_, err := wds.AppsV1().Deployments("nginx").Patch(
				ctx, "nginx", types.MergePatchType, patch, metav1.PatchOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			util.ValidateNumDeployments(ctx, wec1, ns, 0)
			util.ValidateNumDeployments(ctx, wec2, ns, 0)

			ginkgo.By("creating WEC objects when workload object labels resume matching")
			patch = []byte(`{"metadata": {"labels": {"app.kubernetes.io/name": "nginx"}}}`)
			_, err = wds.AppsV1().Deployments("nginx").Patch(
				ctx, "nginx", types.MergePatchType, patch, metav1.PatchOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			util.ValidateNumDeployments(ctx, wec1, ns, 1)
			util.ValidateNumDeployments(ctx, wec2, ns, 1)
		})

		ginkgo.It("handles multiple bindingpolicies with overlapping matches", func(ctx context.Context) {
			ginkgo.By("creating a second bindingpolicy with overlapping matches")
			util.CreateBindingPolicy(ctx, ksWds, "nginx-2",
				[]metav1.LabelSelector{
					{MatchLabels: map[string]string{"location-group": "edge"}},
				},
				[]ksapi.DownsyncPolicyClause{
					{DownsyncObjectTest: ksapi.DownsyncObjectTest{
						ObjectSelectors: []metav1.LabelSelector{{MatchLabels: map[string]string{"app.kubernetes.io/name": "nginx"}}},
					}},
				},
			)
			util.ValidateNumDeployments(ctx, wec1, ns, 1)
			util.ValidateNumDeployments(ctx, wec2, ns, 1)

			ginkgo.By("deleting the second bindingpolicy")
			err := ksWds.ControlV1alpha1().BindingPolicies().Delete(ctx, "nginx-2", metav1.DeleteOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			util.ValidateNumDeployments(ctx, wec1, ns, 1)
			util.ValidateNumDeployments(ctx, wec2, ns, 1)
		})

		ginkgo.It("deletes WEC objects when wds deployment is deleted", func(ctx context.Context) {
			util.DeleteDeployment(ctx, wds, ns, "nginx")
			util.ValidateNumDeployments(ctx, wec1, ns, 0)
			util.ValidateNumDeployments(ctx, wec2, ns, 0)
		})

		ginkgo.It("deletes WEC objects when BindingPolicy is deleted", func(ctx context.Context) {
			err := ksWds.ControlV1alpha1().BindingPolicies().Delete(ctx, "nginx", metav1.DeleteOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			util.ValidateNumDeployments(ctx, wec1, ns, 0)
			util.ValidateNumDeployments(ctx, wec2, ns, 0)
		})

		ginkgo.It("shards a wrapped workload when needed", func(ctx context.Context) {
			util.DeleteDeployment(ctx, wds, ns, "nginx")
			originalArgs := util.ReadContainerArgsInDeployment(ctx, coreCluster, "wds1-system", "transport-controller", "transport-controller")
			originalArgsBytes, err := json.Marshal(originalArgs)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			originalArgsPatch := []byte(fmt.Sprintf(`{"spec":{"template":{"spec":{"containers":[{"args":%s,"name":"transport-controller"}]}}}}`, string(originalArgsBytes)))
			changedArgs := append(originalArgs, "--max-size-wrapped-object=1000")
			changedArgsBytes, err := json.Marshal(changedArgs)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			changedArgsPatch := []byte(fmt.Sprintf(`{"spec":{"template":{"spec":{"containers":[{"args":%s,"name":"transport-controller"}]}}}}`, string(changedArgsBytes)))
			var patched bool
			ginkgo.DeferCleanup(func(ctx context.Context) {
				if patched {
					_, err = coreCluster.AppsV1().Deployments("wds1-system").Patch(ctx, "transport-controller", types.StrategicMergePatchType, originalArgsPatch, metav1.PatchOptions{})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
			})

			ginkgo.By("setting max size of wrapped object to 1000 bytes")
			patched = true
			_, err = coreCluster.AppsV1().Deployments("wds1-system").Patch(ctx, "transport-controller", types.StrategicMergePatchType, changedArgsPatch, metav1.PatchOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			util.WaitForDepolymentAvailability(ctx, coreCluster, "wds1-system", "transport-controller")
			var names = []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"}
			for _, i := range names {
				util.CreateDeployment(ctx, wds, ns, i,
					map[string]string{
						"label1": "test1",
					})
			}
			util.CreateBindingPolicy(ctx, ksWds, "multipledep",
				[]metav1.LabelSelector{
					{MatchLabels: map[string]string{"name": "cluster1"}},
				},
				[]ksapi.DownsyncPolicyClause{
					{DownsyncObjectTest: ksapi.DownsyncObjectTest{
						ObjectSelectors: []metav1.LabelSelector{
							{MatchLabels: map[string]string{"label1": "test1"}},
						},
					}},
				},
			)

			// Expect addon-addon-status-deploy-0  and nginx-wds1 manifest works on both clusters.
			// And the 10 manifestworks for the deployment on cluster1.
			util.ValidateNumManifestworks(ctx, its, "cluster1", 12)
			util.ValidateNumManifestworks(ctx, its, "cluster2", 2)
			util.ValidateNumDeployments(ctx, wec1, ns, 10)
			util.ValidateNumDeployments(ctx, wec2, ns, 0)

			ginkgo.By("updating the bindingpolicy object with sharded wrapped workloads and expecting updated deployments")
			BPPatch := []byte(`{"spec": {"clusterSelectors": [{"matchLabels": {"name": "cluster2"}}]}}`)
			_, err = ksWds.ControlV1alpha1().BindingPolicies().Patch(ctx, "multipledep", types.MergePatchType, BPPatch, metav1.PatchOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			// Expect addon-addon-status-deploy-0  and nginx-wds1 manifest works on both clusters.
			// And the 10 manifestworks for the deployment on cluster2.
			util.ValidateNumManifestworks(ctx, its, "cluster1", 2)
			util.ValidateNumManifestworks(ctx, its, "cluster2", 12)
			util.ValidateNumDeployments(ctx, wec1, ns, 0)
			util.ValidateNumDeployments(ctx, wec2, ns, 10)

			ginkgo.By("deleting the bindingpolicy with sharded wrapped workloads and expecting deployments to get deleted")
			util.DeleteBindingPolicy(ctx, ksWds, "multipledep")
			// Expect addon-addon-status-deploy-0  and nginx-wds1 manifest works on both clusters.
			util.ValidateNumManifestworks(ctx, its, "cluster1", 2)
			util.ValidateNumManifestworks(ctx, its, "cluster2", 2)
			util.ValidateNumDeployments(ctx, wec1, ns, 0)
			util.ValidateNumDeployments(ctx, wec2, ns, 0)
		})

		// 1. Create object 1 with label A and label B, object 2 with label B,
		// and a Placement that matches labels A AND B, and verify that only
		// object 1 matches and is delivered.
		// 2. Patch the cluster selector and expect objects to be removed and downsynced correctly.
		ginkgo.It("supports multi-factor object and cluster selectors", func(ctx context.Context) {
			ginkgo.By("creating two deployments and a bindingpolicy that matches only one")
			util.DeleteDeployment(ctx, wds, ns, "nginx")
			util.CreateDeployment(ctx, wds, ns, "one",
				map[string]string{
					"label1": "test1",
				})
			util.CreateDeployment(ctx, wds, ns, "two",
				map[string]string{
					"label1": "test1",
					"label2": "test2",
				})
			util.CreateBindingPolicy(ctx, ksWds, "both-labels",
				[]metav1.LabelSelector{
					{MatchLabels: map[string]string{"name": "cluster1"}},
				},
				[]ksapi.DownsyncPolicyClause{
					{DownsyncObjectTest: ksapi.DownsyncObjectTest{
						ObjectSelectors: []metav1.LabelSelector{
							{MatchLabels: map[string]string{"label1": "test1", "label2": "test2"}}},
					}},
				},
			)
			util.ValidateNumDeployments(ctx, wec1, ns, 1)
			util.ValidateNumDeployments(ctx, wec2, ns, 0)

			ginkgo.By("patching the cluster selector")
			patch := []byte(`{"spec": {"clusterSelectors": [{"matchLabels": {"name": "cluster2"}}]}}`)
			_, err := ksWds.ControlV1alpha1().BindingPolicies().Patch(
				ctx, "both-labels", types.MergePatchType, patch, metav1.PatchOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			util.ValidateNumDeployments(ctx, wec1, ns, 0)
			util.ValidateNumDeployments(ctx, wec2, ns, 1)
		})

		ginkgo.It("handles clusterSelector with MatchLabels and MatchExpressions", func(ctx context.Context) {
			util.DeleteDeployment(ctx, wds, ns, "nginx")
			util.CreateDeployment(ctx, wds, ns, "one",
				map[string]string{
					"label1": "A",
				})
			util.CreateDeployment(ctx, wds, ns, "two",
				map[string]string{
					"label1": "B",
				})
			util.CreateDeployment(ctx, wds, ns, "three",
				map[string]string{
					"label1": "C",
				})
			util.CreateBindingPolicy(ctx, ksWds, "two-cluster-selectors",
				[]metav1.LabelSelector{
					{MatchLabels: map[string]string{"name": "cluster1"}},
					{MatchExpressions: []metav1.LabelSelectorRequirement{
						{Key: "name", Operator: metav1.LabelSelectorOpIn, Values: []string{"cluster1", "cluster2", "cluster3"}},
					}},
				},
				[]ksapi.DownsyncPolicyClause{
					{DownsyncObjectTest: ksapi.DownsyncObjectTest{
						ObjectSelectors: []metav1.LabelSelector{
							{MatchLabels: map[string]string{"label1": "A"}},
							{MatchLabels: map[string]string{"label1": "B"}},
						},
					}},
				},
			)
			util.ValidateNumDeployments(ctx, wec1, ns, 2)
			util.ValidateNumDeployments(ctx, wec2, ns, 2)
		})

		ginkgo.It("selects workload objects based on both labels and name", func(ctx context.Context) {
			util.DeleteDeployment(ctx, wds, ns, "nginx")
			util.CreateDeployment(ctx, wds, ns, "one",
				map[string]string{
					"label1": "A",
				})
			util.CreateDeployment(ctx, wds, ns, "two",
				map[string]string{
					"label1": "B",
				})
			util.CreateDeployment(ctx, wds, ns, "three",
				map[string]string{
					"label1": "C",
				})
			util.CreateBindingPolicy(ctx, ksWds, "test-name-and-label",
				[]metav1.LabelSelector{
					{MatchLabels: map[string]string{"location-group": "edge"}},
				},
				[]ksapi.DownsyncPolicyClause{
					{DownsyncObjectTest: ksapi.DownsyncObjectTest{
						ObjectNames:     []string{"three"},
						ObjectSelectors: []metav1.LabelSelector{{MatchLabels: map[string]string{"label1": "C"}}},
					}},
				},
			)
			util.ValidateNumDeployments(ctx, wec1, ns, 1)
			util.ValidateNumDeployments(ctx, wec2, ns, 1)
		})
	})

	ginkgo.Context("singleton status creation and deletion", func() {
		ginkgo.It("sets (or deletes) singleton status when a singleton bindingpolicy/deployment is created (or deleted)", func(ctx context.Context) {
			util.DeleteDeployment(ctx, wds, ns, "nginx") // we don't have to delete nginx
			util.CreateDeployment(ctx, wds, ns, "nginx-singleton",
				map[string]string{
					"app.kubernetes.io/name": "nginx-singleton",
				})
			util.CreateBindingPolicy(ctx, ksWds, "nginx-singleton",
				[]metav1.LabelSelector{
					{MatchLabels: map[string]string{"name": "cluster1"}},
				},
				[]ksapi.DownsyncPolicyClause{
					{DownsyncObjectTest: ksapi.DownsyncObjectTest{
						ObjectSelectors: []metav1.LabelSelector{{MatchLabels: map[string]string{"app.kubernetes.io/name": "nginx-singleton"}}},
					}},
				},
				func(bp *ksapi.BindingPolicy) error {
					bp.Spec.WantSingletonReportedState = true
					return nil
				},
			)
			util.ValidateNumDeployments(ctx, wec1, ns, 1)
			util.ValidateNumDeployments(ctx, wec2, ns, 0)
			util.ValidateSingletonStatus(ctx, wds, ns, "nginx-singleton")
			patch := []byte(`{"spec":{"clusterSelectors":[{"matchLabels":{"name":"CelestialNexus"}}]}}`)
			_, err := ksWds.ControlV1alpha1().BindingPolicies().Patch(
				ctx, "nginx-singleton", types.MergePatchType, patch, metav1.PatchOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			util.ValidateNumDeployments(ctx, wec1, ns, 0)
			util.ValidateNumDeployments(ctx, wec2, ns, 0)
			util.ValidateSingletonStatusZeroValue(ctx, wds, ns, "nginx-singleton")
		})
	})

	ginkgo.Context("singleton status eventual consistency", func() {
		ginkgo.It("cleans up previously synced but currently invalid singleton status", func(ctx context.Context) {
			util.DeleteDeployment(ctx, wds, ns, "nginx")
			util.CreateDeployment(ctx, wds, ns, "nginx-singleton",
				map[string]string{
					"app.kubernetes.io/name": "nginx-singleton",
				})
			util.CreateBindingPolicy(ctx, ksWds, "nginx-singleton",
				[]metav1.LabelSelector{
					{MatchLabels: map[string]string{"name": "cluster1"}},
				},
				[]ksapi.DownsyncPolicyClause{
					{DownsyncObjectTest: ksapi.DownsyncObjectTest{
						ObjectSelectors: []metav1.LabelSelector{{MatchLabels: map[string]string{"app.kubernetes.io/name": "nginx-singleton"}}},
					}},
				},
				func(bp *ksapi.BindingPolicy) error {
					bp.Spec.WantSingletonReportedState = true
					return nil
				},
			)
			util.ValidateNumDeployments(ctx, wec1, ns, 1)
			util.ValidateNumDeployments(ctx, wec2, ns, 0)
			util.ValidateSingletonStatus(ctx, wds, ns, "nginx-singleton")
			ginkgo.GinkgoWriter.Println("Singleton status synced")

			originalArgs := util.ReadContainerArgsInDeployment(ctx, coreCluster, "wds1-system", "kubestellar-controller-manager", "manager")
			originalArgsBytes, err := json.Marshal(originalArgs)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			originalArgsPatch := []byte(fmt.Sprintf(`{"spec":{"template":{"spec":{"containers":[{"args":%s,"name":"manager"}]}}}}`, string(originalArgsBytes)))
			changedArgs := append(originalArgs, "--controllers=binding")
			changedArgsBytes, err := json.Marshal(changedArgs)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			var scaledDown, semiCrashed bool
			ginkgo.DeferCleanup(func(ctx context.Context) {
				if semiCrashed {
					_, err = coreCluster.AppsV1().Deployments("wds1-system").Patch(ctx, "kubestellar-controller-manager", types.StrategicMergePatchType, originalArgsPatch, metav1.PatchOptions{})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
				if scaledDown {
					err = util.ScaleDeployment(ctx, coreCluster, "wds1-system", "kubestellar-controller-manager", 1)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
			})

			scaledDown = true
			// Restart the controller manager without starting the status controller.
			err = util.ScaleDeployment(ctx, coreCluster, "wds1-system", "kubestellar-controller-manager", 0)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			util.WaitForDepolymentAvailability(ctx, coreCluster, "wds1-system", "kubestellar-controller-manager")
			changedArgsPatch := []byte(fmt.Sprintf(`{"spec":{"template":{"spec":{"containers":[{"args":%s,"name":"manager"}]}}}}`, string(changedArgsBytes)))
			semiCrashed = true
			_, err = coreCluster.AppsV1().Deployments("wds1-system").Patch(ctx, "kubestellar-controller-manager", types.StrategicMergePatchType, changedArgsPatch, metav1.PatchOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			err = util.ScaleDeployment(ctx, coreCluster, "wds1-system", "kubestellar-controller-manager", 1)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			scaledDown = false
			util.WaitForDepolymentAvailability(ctx, coreCluster, "wds1-system", "kubestellar-controller-manager")

			// At this time, the status controller should not be running, this is a simulation of a crush.
			BPPatch := []byte(`{"spec":{"clusterSelectors":[{"matchLabels":{"name":"CelestialNexus"}}]}}`)
			_, err = ksWds.ControlV1alpha1().BindingPolicies().Patch(
				ctx, "nginx-singleton", types.MergePatchType, BPPatch, metav1.PatchOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			util.ValidateNumDeployments(ctx, wec1, ns, 0)
			util.ValidateNumDeployments(ctx, wec2, ns, 0)

			scaledDown = true
			// Restart the controller manager normally.
			err = util.ScaleDeployment(ctx, coreCluster, "wds1-system", "kubestellar-controller-manager", 0)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			util.WaitForDepolymentAvailability(ctx, coreCluster, "wds1-system", "kubestellar-controller-manager")
			_, err = coreCluster.AppsV1().Deployments("wds1-system").Patch(ctx, "kubestellar-controller-manager", types.StrategicMergePatchType, originalArgsPatch, metav1.PatchOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			semiCrashed = false
			err = util.ScaleDeployment(ctx, coreCluster, "wds1-system", "kubestellar-controller-manager", 1)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			scaledDown = false
			util.WaitForDepolymentAvailability(ctx, coreCluster, "wds1-system", "kubestellar-controller-manager")

			// The status controller, 'recovered' from the simulated crush, should drive the singleton status
			// of the Deployment towards eventual consistency, i.e. clean the 'previously synced but currently invalid' status.
			// That is, we should write func ValidateSingletonStatusZeroValue here instead of ValidateSingletonStatus**Non**ZeroValue.
			// But that would break the CI without the the improved status controller in-place yet.
			// More importantly, this func demonstrates the status controller's current 'eventually **in**consistent' behavior.
			// This func must be replaced by util.ValidateSingletonStatusZeroValue once the improvemets are merged.
			util.ValidateSingletonStatusNonZeroValue(ctx, wds, ns, "nginx-singleton")
			// TODO: Write the line below instead of the line above.
			// util.ValidateSingletonStatusZeroValue(ctx, wds, ns, "nginx-singleton")
		})
	})

	ginkgo.Context("object cleaning", func() {
		ginkgo.It("properly starts a service", func(ctx context.Context) {
			util.CreateService(ctx, wds, ns, "hello-service", "hello-service")
			util.CreateBindingPolicy(ctx, ksWds, "hello-service",
				[]metav1.LabelSelector{
					{MatchLabels: map[string]string{"name": "cluster1"}},
				},
				[]ksapi.DownsyncPolicyClause{
					{DownsyncObjectTest: ksapi.DownsyncObjectTest{
						ObjectSelectors: []metav1.LabelSelector{{MatchLabels: map[string]string{"app.kubernetes.io/name": "hello-service"}}},
					}},
				},
			)
			util.ValidateNumServices(ctx, wec1, ns, 1)
		})
		ginkgo.It("properly starts a job with metadata.generateName", func(ctx context.Context) {
			util.CreateJob(ctx, wds, ns, "hello-job", "hello-job")
			util.CreateBindingPolicy(ctx, ksWds, "hello-job",
				[]metav1.LabelSelector{
					{MatchLabels: map[string]string{"name": "cluster1"}},
				},
				[]ksapi.DownsyncPolicyClause{
					{DownsyncObjectTest: ksapi.DownsyncObjectTest{
						ObjectSelectors: []metav1.LabelSelector{{MatchLabels: map[string]string{"app.kubernetes.io/name": "hello-job"}}},
					}},
				},
			)
			util.ValidateNumJobs(ctx, wec1, ns, 1)
		})
	})

	ginkgo.Context("resiliency testing", func() {
		ginkgo.It("survives WDS coming down", func(ctx context.Context) {
			util.DeletePods(ctx, coreCluster, "wds1-system", "kubestellar")
			util.DeletePods(ctx, coreCluster, "wds1-system", "transport")
			util.ValidateNumDeployments(ctx, wec1, ns, 1)
			util.ValidateNumDeployments(ctx, wec2, ns, 1)
			util.Expect1PodOfEach(ctx, coreCluster, "wds1-system", "kubestellar-controller-manager", "transport-controller")
		})

		ginkgo.It("survives kubeflex coming down", func(ctx context.Context) {
			util.DeletePods(ctx, coreCluster, "kubeflex-system", "")
			util.ValidateNumDeployments(ctx, wec1, ns, 1)
			util.ValidateNumDeployments(ctx, wec2, ns, 1)
			util.Expect1PodOfEach(ctx, coreCluster, "kubeflex-system", "kubeflex-controller-manager", "postgres-postgresql-0")
		})

		ginkgo.It("survives ITS vcluster coming down", func(ctx context.Context) {
			util.DeletePods(ctx, coreCluster, "its1-system", "vcluster")
			util.ValidateNumDeployments(ctx, wec1, ns, 1)
			util.ValidateNumDeployments(ctx, wec2, ns, 1)
		})

		ginkgo.It("survives everything coming down", func(ctx context.Context) {
			ginkgo.By("killing as many pods as possible")
			util.DeletePods(ctx, coreCluster, "wds1-system", "kubestellar")
			util.DeletePods(ctx, coreCluster, "wds1-system", "transport")
			util.DeletePods(ctx, coreCluster, "kubeflex-system", "")
			util.DeletePods(ctx, coreCluster, "its1-system", "vcluster")
			util.ValidateNumDeployments(ctx, wec1, ns, 1)
			util.ValidateNumDeployments(ctx, wec2, ns, 1)

			ginkgo.By("testing that a new deployment still gets downsynced")
			util.CreateDeployment(ctx, wds, ns, "nginx-2",
				map[string]string{
					"app.kubernetes.io/name": "nginx",
				})
			util.ValidateNumDeployments(ctx, wec1, ns, 2)
			util.ValidateNumDeployments(ctx, wec2, ns, 2)
			util.Expect1PodOfEach(ctx, coreCluster, "wds1-system", "kubestellar-controller-manager", "transport-controller")
			util.Expect1PodOfEach(ctx, coreCluster, "kubeflex-system", "kubeflex-controller-manager", "postgres-postgresql-0")
			util.Expect1PodOfEach(ctx, coreCluster, "its1-system", "vcluster")
		})
	})

	ginkgo.Context("combined status testing", func() {
		workloadName := "nginx"
		bpName := "nginx-combinedstatus"
		fullStatusCollectorName := "full-status"
		sumAvailableReplicasStatusCollectorName := "sum-available-replicas"
		selectAvailableStatusCollectorName := "select-available-replicas"
		selectReplicasStatusCollectorName := "replicas"
		listNginxWecsStatusCollectorName := "nginx-wecs"

		clusterSelector := []metav1.LabelSelector{
			{MatchLabels: map[string]string{"location-group": "edge"}},
		}
		testAndStatusCollection := []ksapi.DownsyncPolicyClause{
			{DownsyncObjectTest: ksapi.DownsyncObjectTest{
				ObjectSelectors: []metav1.LabelSelector{{MatchLabels: map[string]string{"app.kubernetes.io/name": workloadName}}},
			},
				StatusCollectors: []string{},
			},
		}

		ginkgo.It("can list the full status from each WEC", func(ctx context.Context) {
			util.CreateStatusCollector(ctx, ksWds, fullStatusCollectorName,
				ksapi.StatusCollectorSpec{
					Select: []ksapi.NamedExpression{
						{
							Name: "wecName",
							Def:  "inventory.name",
						},
						{
							Name: "status",
							Def:  "returned.status",
						},
					},
					Limit: 20,
				})

			testAndStatusCollection[0].StatusCollectors = []string{fullStatusCollectorName}
			util.CreateBindingPolicy(ctx, ksWds, bpName, clusterSelector, testAndStatusCollection)

			cs := util.GetCombinedStatus(ctx, ksWds, wds, ns, workloadName, bpName)

			// Validate CombinedStatus results
			gomega.Expect(len(cs.Results)).To(gomega.Equal(1))
			gomega.Expect(cs.Results[0].Name).To(gomega.Equal(fullStatusCollectorName))

			gomega.Expect(len(cs.Results[0].ColumnNames)).To(gomega.Equal(2))
			gomega.Expect(cs.Results[0].ColumnNames[0]).To(gomega.Equal("wecName"))
			gomega.Expect(cs.Results[0].ColumnNames[1]).To(gomega.Equal("status"))

			gomega.Expect(len(cs.Results[0].Rows)).To(gomega.Equal(2))
			gomega.Expect(len(cs.Results[0].Rows[0].Columns)).To(gomega.Equal(2))
			gomega.Expect(len(cs.Results[0].Rows[1].Columns)).To(gomega.Equal(2))

			// we don't know which row will hold data for which WEC
			row0expectedWec := "cluster1"
			row1expectedWec := "cluster2"
			if *cs.Results[0].Rows[0].Columns[0].String != row0expectedWec {
				row0expectedWec = "cluster2"
				row1expectedWec = "cluster1"
			}

			gomega.Expect(*cs.Results[0].Rows[0].Columns[0].String).To(gomega.Equal(row0expectedWec))
			gomega.Expect(cs.Results[0].Rows[0].Columns[1].Object).Should(gomega.Not(gomega.BeNil()))
			gomega.Expect(*cs.Results[0].Rows[1].Columns[0].String).To(gomega.Equal(row1expectedWec))
			gomega.Expect(cs.Results[0].Rows[1].Columns[1].Object).Should(gomega.Not(gomega.BeNil()))
		})

		ginkgo.It("can sum status.availableReplicas across WECs", func(ctx context.Context) {
			availableReplicasCEL := ksapi.Expression("returned.status.availableReplicas")
			util.CreateStatusCollector(ctx, ksWds, sumAvailableReplicasStatusCollectorName,
				ksapi.StatusCollectorSpec{
					CombinedFields: []ksapi.NamedAggregator{{
						Name:    "sum",
						Subject: &availableReplicasCEL,
						Type:    ksapi.AggregatorTypeSum,
					}},
					Limit: 10,
				})

			testAndStatusCollection[0].StatusCollectors = []string{sumAvailableReplicasStatusCollectorName}
			util.CreateBindingPolicy(ctx, ksWds, bpName, clusterSelector, testAndStatusCollection)

			cs := util.GetCombinedStatus(ctx, ksWds, wds, ns, workloadName, bpName)

			// Validate CombinedStatus results
			gomega.Expect(len(cs.Results)).To(gomega.Equal(1))
			gomega.Expect(cs.Results[0].Name).To(gomega.Equal(sumAvailableReplicasStatusCollectorName))

			gomega.Expect(len(cs.Results[0].ColumnNames)).To(gomega.Equal(1))
			gomega.Expect(cs.Results[0].ColumnNames[0]).To(gomega.Equal("sum"))

			gomega.Expect(len(cs.Results[0].Rows)).To(gomega.Equal(1))
			gomega.Expect(len(cs.Results[0].Rows[0].Columns)).To(gomega.Equal(1))
			gomega.Expect(*cs.Results[0].Rows[0].Columns[0].Number).To(gomega.Equal("2"))
		})

		ginkgo.It("can list all the WECs where the reported number of availableReplicas equals the desired number of replicas", func(ctx context.Context) {

			availableNginxCEL := ksapi.Expression("obj.spec.replicas == returned.status.availableReplicas")
			util.CreateStatusCollector(ctx, ksWds, listNginxWecsStatusCollectorName,
				ksapi.StatusCollectorSpec{
					Filter: &availableNginxCEL,
					Select: []ksapi.NamedExpression{
						{
							Name: "wecName",
							Def:  "inventory.name",
						},
					},
					Limit: 10,
				})

			testAndStatusCollection[0].StatusCollectors = []string{listNginxWecsStatusCollectorName}
			util.CreateBindingPolicy(ctx, ksWds, bpName, clusterSelector, testAndStatusCollection)

			cs := util.GetCombinedStatus(ctx, ksWds, wds, ns, workloadName, bpName)

			// Validate CombinedStatus results
			gomega.Expect(len(cs.Results)).To(gomega.Equal(1))
			gomega.Expect(cs.Results[0].Name).To(gomega.Equal(listNginxWecsStatusCollectorName))

			gomega.Expect(len(cs.Results[0].ColumnNames)).To(gomega.Equal(1))
			gomega.Expect(cs.Results[0].ColumnNames[0]).To(gomega.Equal("wecName"))

			gomega.Expect(len(cs.Results[0].Rows)).To(gomega.Equal(2))

			// we don't know which row will hold data for which WEC
			row0expectedWec := "cluster1"
			row1expectedWec := "cluster2"
			if *cs.Results[0].Rows[0].Columns[0].String != row0expectedWec {
				row0expectedWec = "cluster2"
				row1expectedWec = "cluster1"
			}
			gomega.Expect(*cs.Results[0].Rows[0].Columns[0].String).To(gomega.Equal(row0expectedWec))
			gomega.Expect(*cs.Results[0].Rows[1].Columns[0].String).To(gomega.Equal(row1expectedWec))
		})

		ginkgo.It("can support multiple StatusCollectors", func(ctx context.Context) {
			util.CreateStatusCollector(ctx, ksWds, selectAvailableStatusCollectorName,
				ksapi.StatusCollectorSpec{
					Select: []ksapi.NamedExpression{
						{
							Name: "wecName",
							Def:  "inventory.name",
						}, {
							Name: "availableReplicas",
							Def:  "returned.status.availableReplicas",
						}},
					Limit: 20,
				})

			util.CreateStatusCollector(ctx, ksWds, selectReplicasStatusCollectorName,
				ksapi.StatusCollectorSpec{
					Select: []ksapi.NamedExpression{
						{
							Name: "wecName",
							Def:  "inventory.name",
						},
						{
							Name: "replicas",
							Def:  "returned.status.replicas",
						}},
					Limit: 20,
				})

			testAndStatusCollection[0].StatusCollectors = []string{selectAvailableStatusCollectorName, selectReplicasStatusCollectorName}
			util.CreateBindingPolicy(ctx, ksWds, bpName, clusterSelector, testAndStatusCollection)

			cs := util.GetCombinedStatus(ctx, ksWds, wds, ns, workloadName, bpName)

			// Validate CombinedStatus results
			gomega.Expect(len(cs.Results)).To(gomega.Equal(2))

			gomega.Expect(len(cs.Results[0].ColumnNames)).To(gomega.Equal(2))
			gomega.Expect(len(cs.Results[0].Rows)).To(gomega.Equal(2))

			gomega.Expect(len(cs.Results[1].ColumnNames)).To(gomega.Equal(2))
			gomega.Expect(len(cs.Results[1].Rows)).To(gomega.Equal(2))

		})
	})
})
