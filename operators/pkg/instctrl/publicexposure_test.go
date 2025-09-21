// Copyright 2020-2025 Politecnico di Torino
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package instctrl_test

import (
	"context"
	crand "crypto/rand"
	"fmt"
	"math/big"
	mrand "math/rand"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clv1alpha2 "github.com/netgroup-polito/CrownLabs/operators/api/v1alpha2"
	clctx "github.com/netgroup-polito/CrownLabs/operators/pkg/context"
	"github.com/netgroup-polito/CrownLabs/operators/pkg/forge"
	"github.com/netgroup-polito/CrownLabs/operators/pkg/instctrl"
)

var _ = Describe("Public Exposure Functions", func() {
	ctx := context.Background()

	var (
		reconciler *instctrl.InstanceReconciler
		instance   *clv1alpha2.Instance
		template   *clv1alpha2.Template
		namespace  string
		testIPPool []string
		portBase   int
	)

	getRandomIP := func() string {
		pool := testIPPool
		r := mrand.New(mrand.NewSource(time.Now().UnixNano()))
		return pool[r.Intn(len(pool))]
	}

	BeforeEach(func() {
		randomNum, _ := crand.Int(crand.Reader, big.NewInt(100000))
		namespace = fmt.Sprintf("test-namespace-%d", randomNum.Int64())

		// Create a unique IP pool for this test to avoid conflicts
		baseIP := fmt.Sprintf("192.168.%d", 100+randomNum.Int64()%100)
		testIPPool = []string{
			fmt.Sprintf("%s.1", baseIP),
			fmt.Sprintf("%s.2", baseIP),
			fmt.Sprintf("%s.3", baseIP),
			fmt.Sprintf("%s.4", baseIP),
		}

		// Use unique port base for this test
		portBase = 10000 + int(randomNum.Int64()%50000)

		reconciler = &instctrl.InstanceReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			PublicExposureOpts: forge.PublicExposureOpts{
				IPPool: testIPPool,
				CommonAnnotations: map[string]string{
					"metallb.universe.tf/allow-shared-ip": "public-exposure",
					"metallb.universe.tf/address-pool":    "public",
				},
				LoadBalancerIPsKey: "metallb.universe.tf/loadBalancerIPs",
			},
		}

		template = &clv1alpha2.Template{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-template",
				Namespace: namespace,
			},
			Spec: clv1alpha2.TemplateSpec{
				AllowPublicExposure: true,
				PrettyName:          "Test Template",
				Description:         "A test template for public exposure tests",
				EnvironmentList: []clv1alpha2.Environment{
					{
						Name:            "test-env",
						Image:           "nginx:latest",
						EnvironmentType: clv1alpha2.ClassContainer,
						GuiEnabled:      false,
						Persistent:      false,
						Resources: clv1alpha2.EnvironmentResources{
							CPU:                   1,
							ReservedCPUPercentage: 50,
							Memory:                resource.MustParse("512Mi"),
						},
					},
				},
			},
		}

		instance = &clv1alpha2.Instance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-instance",
				Namespace: namespace,
			},
			Spec: clv1alpha2.InstanceSpec{
				Running: true,
				PublicExposure: &clv1alpha2.InstancePublicExposure{
					Ports: []clv1alpha2.PublicServicePort{
						{Name: "http", Port: int32(portBase + 80), TargetPort: 80},
					},
				},
			},
		}

		instance.Annotations = map[string]string{"metallb.universe.tf/loadBalancerIPs": getRandomIP()}

		ns := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		Expect(k8sClient.Create(ctx, template)).To(Succeed())
		Expect(k8sClient.Create(ctx, instance)).To(Succeed())
	})

	AfterEach(func() {
		// Clean up all services in the namespace first
		svcList := &v1.ServiceList{}
		err := k8sClient.List(ctx, svcList, client.InNamespace(namespace))
		if err == nil {
			for i := range svcList.Items {
				svc := &svcList.Items[i]
				_ = k8sClient.Delete(ctx, svc)
			}
		}

		// Wait for service deletion to complete
		Eventually(func() bool {
			svcList := &v1.ServiceList{}
			err := k8sClient.List(ctx, svcList, client.InNamespace(namespace))
			return err != nil || len(svcList.Items) == 0
		}, "10s", "100ms").Should(BeTrue())

		// Then delete the namespace
		ns := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
		Expect(k8sClient.Delete(ctx, ns)).To(Succeed())

		// Wait for namespace cleanup to complete
		time.Sleep(1 * time.Second)
	})

	Describe("EnforcePublicExposure", func() {
		Context("When public exposure should be present", func() {
			BeforeEach(func() {
				ctx, _ = clctx.InstanceInto(ctx, instance)
				ctx, _ = clctx.TemplateInto(ctx, template)
			})

			It("Should create LoadBalancer service when conditions are met", func() {
				err := reconciler.EnforcePublicExposure(ctx)
				Expect(err).ToNot(HaveOccurred())

				svcName := forge.LoadBalancerServiceName(instance)
				service := &v1.Service{}
				err = k8sClient.Get(ctx, types.NamespacedName{Name: svcName, Namespace: namespace}, service)
				Expect(err).ToNot(HaveOccurred())
				Expect(service.Spec.Type).To(Equal(v1.ServiceTypeLoadBalancer))
			})

			It("Should update instance status correctly", func() {
				err := reconciler.EnforcePublicExposure(ctx)
				Expect(err).ToNot(HaveOccurred())

				// Verify status was updated
				Expect(instance.Status.PublicExposure).ToNot(BeNil())
				Expect(instance.Status.PublicExposure.Phase).To(Equal(clv1alpha2.PublicExposurePhaseReady))
				Expect(instance.Status.PublicExposure.ExternalIP).ToNot(BeEmpty())
				Expect(instance.Status.PublicExposure.Ports).To(HaveLen(1))
				Expect(instance.Status.PublicExposure.Ports[0].Port).To(Equal(int32(portBase + 80)))
			})

			It("Should handle automatic port assignment", func() {
				instance.Spec.PublicExposure.Ports = []clv1alpha2.PublicServicePort{
					{Name: "auto1", Port: 0, TargetPort: 80},
					{Name: "auto2", Port: 0, TargetPort: 90},
				}
				Expect(k8sClient.Update(ctx, instance)).To(Succeed())
				ctx, _ = clctx.InstanceInto(ctx, instance)

				err := reconciler.EnforcePublicExposure(ctx)
				Expect(err).ToNot(HaveOccurred())

				svcName := forge.LoadBalancerServiceName(instance)
				service := &v1.Service{}
				err = k8sClient.Get(ctx, types.NamespacedName{Name: svcName, Namespace: namespace}, service)
				Expect(err).ToNot(HaveOccurred())
				Expect(service.Spec.Ports).To(HaveLen(2))
				// Automatic ports should be assigned from base port
				Expect(service.Spec.Ports[0].Port).To(BeNumerically(">=", 30000))
				Expect(service.Spec.Ports[1].Port).To(BeNumerically(">=", 30000))
			})
		})

		Context("When public exposure should be absent", func() {
			BeforeEach(func() {
				ctx, _ = clctx.InstanceInto(ctx, instance)
				ctx, _ = clctx.TemplateInto(ctx, template)
			})

			It("Should remove service when template doesn't allow public exposure", func() {
				// First create the service
				err := reconciler.EnforcePublicExposure(ctx)
				Expect(err).ToNot(HaveOccurred())

				// Then disable public exposure in template
				template.Spec.AllowPublicExposure = false
				Expect(k8sClient.Update(ctx, template)).To(Succeed())
				ctx, _ = clctx.TemplateInto(ctx, template)

				err = reconciler.EnforcePublicExposure(ctx)
				Expect(err).ToNot(HaveOccurred())

				// Service should be removed
				svcName := forge.LoadBalancerServiceName(instance)
				service := &v1.Service{}
				err = k8sClient.Get(ctx, types.NamespacedName{Name: svcName, Namespace: namespace}, service)
				Expect(err).To(HaveOccurred())
			})

			It("Should remove service when instance is not running", func() {
				// First create the service
				err := reconciler.EnforcePublicExposure(ctx)
				Expect(err).ToNot(HaveOccurred())

				// Then stop the instance
				instance.Spec.Running = false
				Expect(k8sClient.Update(ctx, instance)).To(Succeed())
				ctx, _ = clctx.InstanceInto(ctx, instance)

				err = reconciler.EnforcePublicExposure(ctx)
				Expect(err).ToNot(HaveOccurred())

				// Service should be removed
				svcName := forge.LoadBalancerServiceName(instance)
				service := &v1.Service{}
				err = k8sClient.Get(ctx, types.NamespacedName{Name: svcName, Namespace: namespace}, service)
				Expect(err).To(HaveOccurred())
			})
		})

		Context("Error handling", func() {
			BeforeEach(func() {
				ctx, _ = clctx.InstanceInto(ctx, instance)
				ctx, _ = clctx.TemplateInto(ctx, template)
			})

			It("Should error on duplicate ports in public exposure", func() {
				instance.Spec.PublicExposure.Ports = []clv1alpha2.PublicServicePort{
					{Name: "http", Port: int32(portBase + 80), TargetPort: 80},
					{Name: "http2", Port: int32(portBase + 80), TargetPort: 81}, // Duplicate port
				}
				Expect(k8sClient.Update(ctx, instance)).To(Succeed())
				ctx, _ = clctx.InstanceInto(ctx, instance)

				err := reconciler.EnforcePublicExposure(ctx)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("duplicate requested port"))
			})

			It("Should error on duplicate targetPorts in public exposure", func() {
				instance.Spec.PublicExposure.Ports = []clv1alpha2.PublicServicePort{
					{Name: "http", Port: int32(portBase + 80), TargetPort: 80},
					{Name: "http2", Port: int32(portBase + 81), TargetPort: 80}, // Duplicate targetPort
				}
				Expect(k8sClient.Update(ctx, instance)).To(Succeed())
				ctx, _ = clctx.InstanceInto(ctx, instance)

				err := reconciler.EnforcePublicExposure(ctx)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("duplicate desired targetPort"))
			})

			It("Should error if requested port is already used on all IPs", func() {
				// Use a range of ports to saturate all IPs completely
				basePort := int32(portBase + 1000)
				portsPerIP := 50 // Create many ports per IP to ensure saturation

				// Create multiple services on each IP to saturate the available ports
				var createdServices []*v1.Service
				for i, ip := range reconciler.PublicExposureOpts.IPPool {
					for j := 0; j < portsPerIP; j++ {
						// Use the SAME port numbers on ALL IPs, not different ranges per IP
						port := basePort + int32(j)
						svc := &v1.Service{
							ObjectMeta: metav1.ObjectMeta{
								Name:      fmt.Sprintf("blocker-svc-%d-%d", i, j),
								Namespace: namespace,
								Labels:    forge.LoadBalancerServiceLabels(),
								Annotations: map[string]string{
									reconciler.PublicExposureOpts.LoadBalancerIPsKey: ip,
								},
							},
							Spec: v1.ServiceSpec{
								Type:  v1.ServiceTypeLoadBalancer,
								Ports: []v1.ServicePort{{Name: "blocker", Port: port, TargetPort: intstr.FromInt(80)}},
							},
						}
						Expect(k8sClient.Create(ctx, svc)).To(Succeed())
						createdServices = append(createdServices, svc)
					}
				}

				// Wait for all services to be created
				expectedServiceCount := len(reconciler.PublicExposureOpts.IPPool) * portsPerIP
				Eventually(func() int {
					svcList := &v1.ServiceList{}
					err := k8sClient.List(ctx, svcList, client.InNamespace(namespace), client.MatchingLabels(forge.LoadBalancerServiceLabels()))
					if err != nil {
						return 0
					}
					return len(svcList.Items)
				}, "10s", "100ms").Should(Equal(expectedServiceCount))

				// Verify that at least some conflict ports are actually detected as used on all IPs
				Eventually(func() bool {
					usedPortsByIP, err := instctrl.UpdateUsedPortsByIP(ctx, k8sClient, "", "", &reconciler.PublicExposureOpts)
					if err != nil {
						return false
					}

					// Print detailed debug information
					GinkgoWriter.Printf("DEBUG: UsedPortsByIP map contains %d IPs\n", len(usedPortsByIP))
					for ip, ports := range usedPortsByIP {
						GinkgoWriter.Printf("DEBUG: IP %s has %d ports used\n", ip, len(ports))
						conflictPortStart := basePort
						conflictPortEnd := basePort + int32(portsPerIP)
						conflictsFound := 0
						for port := conflictPortStart; port < conflictPortEnd; port++ {
							if ports[port] {
								conflictsFound++
							}
						}
						GinkgoWriter.Printf("DEBUG: IP %s has %d conflicts in range %d-%d\n", ip, conflictsFound, conflictPortStart, conflictPortEnd-1)
					}

					// Check that ALL IPs have the SAME port conflicts (since we create the same ports on all IPs)
					allIPsHaveConflicts := true
					expectedConflicts := portsPerIP
					for _, ip := range reconciler.PublicExposureOpts.IPPool {
						if ports, exists := usedPortsByIP[ip]; !exists {
							allIPsHaveConflicts = false
							break
						} else {
							// Count conflicts in our specific range
							conflictsFound := 0
							for port := basePort; port < basePort+int32(portsPerIP); port++ {
								if ports[port] {
									conflictsFound++
								}
							}
							if conflictsFound < expectedConflicts {
								allIPsHaveConflicts = false
								break
							}
						}
					}
					return allIPsHaveConflicts
				}, "15s", "500ms").Should(BeTrue(), "Expected significant port conflicts to be detected on all IPs")

				// Now request a specific port that we know is occupied on all IPs
				conflictPort := basePort + int32(portsPerIP/2) // Pick a port from the middle of our occupied range
				instance.Spec.PublicExposure.Ports = []clv1alpha2.PublicServicePort{{Name: "conflict", Port: conflictPort, TargetPort: 80}}
				Expect(k8sClient.Update(ctx, instance)).To(Succeed())
				ctx, _ = clctx.InstanceInto(ctx, instance)

				err := reconciler.EnforcePublicExposure(ctx)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("no available IP can support all requested ports"))
			})
		})
	})
})
