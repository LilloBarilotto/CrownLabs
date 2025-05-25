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

// Package instctrl groups the functionalities related to the Instance controller.
package instctrl

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/trace"
	virtv1 "kubevirt.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	clv1alpha2 "github.com/netgroup-polito/CrownLabs/operators/api/v1alpha2"
	clctx "github.com/netgroup-polito/CrownLabs/operators/pkg/context"
	"github.com/netgroup-polito/CrownLabs/operators/pkg/forge"
	"github.com/netgroup-polito/CrownLabs/operators/pkg/utils"
)

const (
	metallbPoolName = "my-ip-pool"
	sharedIPValue   = "true"
	basePort        = 30000
)

// InstanceReconciler reconciles an Instance object.
type InstanceReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	EventsRecorder     record.EventRecorder
	NamespaceWhitelist metav1.LabelSelector
	ServiceUrls        ServiceUrls
	ContainerEnvOpts   forge.ContainerEnvOpts

	// This function, if configured, is deferred at the beginning of the Reconcile.
	// Specifically, it is meant to be set to GinkgoRecover during the tests,
	// in order to lead to a controlled failure in case the Reconcile panics.
	ReconcileDeferHook func()
}

// ServiceUrls holds URL parameters for the instance reconciler.
type ServiceUrls struct {
	WebsiteBaseURL   string
	InstancesAuthURL string
}

// Reconcile reconciles the state of an Instance resource.
func (r *InstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	if r.ReconcileDeferHook != nil {
		defer r.ReconcileDeferHook()
	}

	log := ctrl.LoggerFrom(ctx, "instance", req.NamespacedName)

	tracer := trace.New("reconcile", trace.Field{Key: "instance", Value: req.NamespacedName})
	ctx = trace.ContextWithTrace(ctx, tracer)
	defer tracer.LogIfLong(utils.LongThreshold())

	// Get the instance object.
	var instance clv1alpha2.Instance
	if err = r.Get(ctx, req.NamespacedName, &instance); err != nil {
		if !kerrors.IsNotFound(err) {
			log.Error(err, "failed retrieving instance")
		}
		// Reconcile was triggered by a delete request.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Check the selector label, in order to know whether to perform or not reconciliation.
	if proceed, err := utils.CheckSelectorLabel(ctrl.LoggerInto(ctx, log), r.Client, instance.GetNamespace(), r.NamespaceWhitelist.MatchLabels); !proceed {
		// If there was an error while checking, show the error and try again.
		if err != nil {
			log.Error(err, "failed checking selector labels")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Add the retrieved instance as part of the context.
	ctx, _ = clctx.InstanceInto(ctx, &instance)
	tracer.Step("retrieved the instance")

	// Defer the function to update the instance status depending on the modifications
	// performed while enforcing the desired environments. This is deferred early to
	// allow setting the CreationLoopBackOff phase in case of errors.
	defer func(original, updated *clv1alpha2.Instance) {
		// If the reconciliation failed with an error, set the instance phase to CreationLoopBackOff.
		// Do not set the CreationLoopBackOff phase in case of conflicts, to prevent transients.
		if err != nil && !kerrors.IsConflict(err) {
			instance.Status.Phase = clv1alpha2.EnvironmentPhaseCreationLoopBackoff
		}

		// Avoid triggering the status update if not necessary.
		if !reflect.DeepEqual(original.Status, updated.Status) {
			if err2 := r.Status().Patch(ctx, updated, client.MergeFrom(original)); err2 != nil {
				log.Error(err2, "failed to update the instance status")
				err = err2
			} else {
				tracer.Step("instance status updated")
				log.Info("instance status correctly updated")
			}
		}
	}(instance.DeepCopy(), &instance)

	// Retrieve the template associated with the current instance.
	templateName := types.NamespacedName{
		Namespace: instance.Spec.Template.Namespace,
		Name:      instance.Spec.Template.Name,
	}
	var template clv1alpha2.Template
	if err := r.Get(ctx, templateName, &template); err != nil {
		log.Error(err, "failed retrieving the instance template", "template", templateName)
		r.EventsRecorder.Eventf(&instance, v1.EventTypeWarning, EvTmplNotFound, EvTmplNotFoundMsg, templateName.Namespace, templateName.Name)
		return ctrl.Result{}, err
	}
	ctx, log = clctx.TemplateInto(ctx, &template)
	tracer.Step("retrieved the instance template")
	log.Info("successfully retrieved the instance template")

	// Retrieve the tenant associated with the current instance.
	tenantName := types.NamespacedName{Name: instance.Spec.Tenant.Name}
	var tenant clv1alpha2.Tenant
	if err := r.Get(ctx, tenantName, &tenant); err != nil {
		log.Error(err, "failed retrieving the instance tenant", "tenant", tenantName)
		r.EventsRecorder.Eventf(&instance, v1.EventTypeWarning, EvTntNotFound, EvTntNotFoundMsg, tenantName.Name)
		return ctrl.Result{}, err
	}
	ctx, log = clctx.TenantInto(ctx, &tenant)
	tracer.Step("retrieved the instance tenant")
	log.Info("successfully retrieved the instance tenant")

	// Patch the instance labels to allow for easier categorization.
	labels, updated := forge.InstanceLabels(instance.GetLabels(), &template, &instance)
	if updated || instance.Spec.PrettyName == "" {
		original := instance.DeepCopy()
		if instance.Spec.PrettyName == "" {
			instance.Spec.PrettyName = forge.RandomInstancePrettyName()
		}
		instance.SetLabels(labels)
		if err := r.Patch(ctx, &instance, client.MergeFrom(original)); err != nil {
			log.Error(err, "failed to update the instance labels")
			return ctrl.Result{}, err
		}
		tracer.Step("instance labels updated")
		log.Info("instance labels correctly configured")
	}

	// Iterate over and enforce the instance environments.
	if err := r.enforceEnvironments(ctx); err != nil {
		log.Error(err, "failed to enforce instance environments")
		return ctrl.Result{}, err
	}

	// Handle public exposure (LoadBalancer) if requested.
	if instance.Spec.PublicExposure != nil {
		if err := r.reconcileInstanceExposure(ctx, &instance); err != nil {
			log.Error(err, "failed to reconcile instance public exposure")
			return ctrl.Result{}, err
		}
	} else {
		// Cleanup any existing LoadBalancer if public exposure is no longer required.
		if err := r.cleanupInstanceExposure(ctx, &instance); err != nil {
			log.Error(err, "failed to cleanup instance public exposure")
			return ctrl.Result{}, err
		}
	}

	if err = r.podScheduleStatusIntoInstance(ctx, &instance); err != nil {
		log.Error(err, "unable to retrieve pod schedule status")
	}

	tracer.Step("instance environments enforced")
	log.Info("instance environments correctly enforced")

	return ctrl.Result{}, nil
}

func (r *InstanceReconciler) enforceEnvironments(ctx context.Context) error {
	instance := clctx.InstanceFrom(ctx)
	template := clctx.TemplateFrom(ctx)

	for i := range template.Spec.EnvironmentList {
		environment := &template.Spec.EnvironmentList[i]
		ctx, log := clctx.EnvironmentInto(ctx, environment)

		// Currently, only instances composed of a single environment are supported.
		// Nonetheless, we return nil in the end, since it is useless to retry later.
		if i >= 1 {
			err := fmt.Errorf("instances composed of multiple environments are currently not supported")
			log.Error(err, "failed to process environment")
			return nil
		}

		switch template.Spec.EnvironmentList[i].EnvironmentType {
		case clv1alpha2.ClassVM, clv1alpha2.ClassCloudVM:
			if err := r.EnforceVMEnvironment(ctx); err != nil {
				r.EventsRecorder.Eventf(instance, v1.EventTypeWarning, EvEnvironmentErr, EvEnvironmentErrMsg, environment.Name)
				return err
			}
		case clv1alpha2.ClassContainer, clv1alpha2.ClassStandalone:
			if err := r.EnforceContainerEnvironment(ctx); err != nil {
				r.EventsRecorder.Eventf(instance, v1.EventTypeWarning, EvEnvironmentErr, EvEnvironmentErrMsg, environment.Name)
				return err
			}
		}

		r.setInitialReadyTimeIfNecessary(ctx)
	}
	return nil
}

// setInitialReadyTimeIfNecessary configures the instance InitialReadyTime status value and emits the corresponding
// prometheus metric, in case it was not already present and the instance is currently ready.
func (r *InstanceReconciler) setInitialReadyTimeIfNecessary(ctx context.Context) {
	instance := clctx.InstanceFrom(ctx)
	if instance.Status.Phase != clv1alpha2.EnvironmentPhaseReady || instance.Status.InitialReadyTime != "" {
		return
	}

	duration := time.Since(instance.GetCreationTimestamp().Time).Truncate(time.Second)
	instance.Status.InitialReadyTime = duration.String()

	// Filter out possible outliers from the prometheus metrics.
	if duration > 30*time.Minute {
		return
	}

	template := clctx.TemplateFrom(ctx)
	environment := clctx.EnvironmentFrom(ctx)

	metricInitialReadyTimes.With(prometheus.Labels{
		metricInitialReadyTimesLabelWorkspace:   template.Spec.WorkspaceRef.Name,
		metricInitialReadyTimesLabelTemplate:    template.GetName(),
		metricInitialReadyTimesLabelEnvironment: environment.Name,
		metricInitialReadyTimesLabelType:        string(environment.EnvironmentType),
		metricInitialReadyTimesLabelPersistent:  strconv.FormatBool(environment.Persistent),
	}).Observe(duration.Seconds())
}

// SetupWithManager registers a new controller for Instance resources.
func (r *InstanceReconciler) SetupWithManager(mgr ctrl.Manager, concurrency int) error {
	mgr.GetLogger().Info("setup manager")
	return ctrl.NewControllerManagedBy(mgr).
		For(&clv1alpha2.Instance{}).
		Owns(&appsv1.Deployment{}).
		Owns(&virtv1.VirtualMachine{}).
		Owns(&v1.Service{}). // Add this line to watch services
		// Here, we use Watches instead of Owns since we need to react also in case a VMI generated from a VM is updated,
		// to correctly update the instance phase in case of persistent VMs with resource quota exceeded.
		Watches(&virtv1.VirtualMachineInstance{}, handler.EnqueueRequestsFromMapFunc(r.vmiToInstance)).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: concurrency,
		}).
		WithLogConstructor(utils.LogConstructor(mgr.GetLogger(), "Instance")).
		Complete(r)
}

// vmiToInstance returns a reconcile request for the instance associated with the given VMI object.
func (r *InstanceReconciler) vmiToInstance(_ context.Context, o client.Object) []reconcile.Request {
	if instance, found := forge.InstanceNameFromLabels(o.GetLabels()); found {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: o.GetNamespace(), Name: instance}}}
	}

	return nil
}

// InstanceExposure functions

// reconcileInstanceExposure creates/updates the LoadBalancer Service for the Instance.
func (r *InstanceReconciler) reconcileInstanceExposure(ctx context.Context, instance *clv1alpha2.Instance) error {
	log := ctrl.LoggerFrom(ctx)
	log.Info("Reconciling instance exposure", "instance", instance.Name)

	// 1. Get existing LoadBalancer Service if any
	svcName := fmt.Sprintf("instance-lb-%s", instance.Name)
	existingSvc := &v1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: svcName, Namespace: instance.Namespace}, existingSvc)
	svcExists := err == nil

	// 2. Verify required exposure configuration
	if instance.Spec.PublicExposure == nil || len(instance.Spec.PublicExposure.Services) == 0 {
		if svcExists {
			return r.Delete(ctx, existingSvc)
		}
		return nil
	}

	// 3. Get used ports
	usedPortsByIP, err := r.updateUsedPortsByIP(ctx, instance.Namespace)
	if err != nil {
		return err
	}

	// 4. Find best IP and assign ports
	targetIP, assignedPorts, err := r.findBestIPAndAssignPorts(ctx, instance, usedPortsByIP)
	if err != nil {
		return err
	}

	// 5. Create/update Service
	var svc *v1.Service
	if svcExists {
		svc = existingSvc
	} else {
		svc = &v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      svcName,
				Namespace: instance.Namespace,
				OwnerReferences: []metav1.OwnerReference{
					*metav1.NewControllerRef(instance, clv1alpha2.GroupVersion.WithKind("Instance")),
				},
			},
			Spec: v1.ServiceSpec{
				Type: v1.ServiceTypeLoadBalancer,
				Selector: map[string]string{
					"crownlabs.polito.it/instance": instance.Name,
				},
			},
		}
	}

	// Set annotations for MetalLB
	if svc.Annotations == nil {
		svc.Annotations = make(map[string]string)
	}
	svc.Annotations["metallb.universe.tf/address-pool"] = metallbPoolName
	svc.Annotations["metallb.universe.tf/allow-shared-ip"] = sharedIPValue
	svc.Annotations["metallb.universe.tf/loadBalancerIPs"] = targetIP

	// Configure service ports
	svc.Spec.Ports = []v1.ServicePort{}
	for _, p := range assignedPorts {
		svc.Spec.Ports = append(svc.Spec.Ports, v1.ServicePort{
			Name:       p.Name,
			Port:       p.AssignedPort,
			TargetPort: intstr.FromInt(int(p.TargetPort)),
			Protocol:   v1.ProtocolTCP,
		})
	}

	// Create or update the service
	if svcExists {
		log.Info("Updating existing LoadBalancer service", "service", svcName)
		if err := r.Update(ctx, svc); err != nil {
			return err
		}
	} else {
		log.Info("Creating new LoadBalancer service", "service", svcName)
		if err := r.Create(ctx, svc); err != nil {
			return err
		}
	}

	log.Info("Service exposure reconciled", "service", svcName, "ip", targetIP)
	return nil
}

// cleanupInstanceExposure removes the LoadBalancer Service if no longer required.
func (r *InstanceReconciler) cleanupInstanceExposure(ctx context.Context, instance *clv1alpha2.Instance) error {
	log := ctrl.LoggerFrom(ctx)
	svcName := fmt.Sprintf("instance-lb-%s", instance.Name)

	svc := &v1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: svcName, Namespace: instance.Namespace}, svc)
	if err == nil {
		// Service exists, delete it
		log.Info("Removing LoadBalancer service", "service", svcName)
		return r.Delete(ctx, svc)
	}
	return client.IgnoreNotFound(err)
}

// getMetalLBIPPool retrieves the IP pool configured in MetalLB
func (r *InstanceReconciler) getMetalLBIPPool(ctx context.Context) ([]string, error) {
	// TODO: Implement logic to get IP pool from MetalLB in the cluster
	// For now, returning a static pool
	return []string{
		"172.18.0.240", "172.18.0.241", "172.18.0.242", "172.18.0.243",
		"172.18.0.244", "172.18.0.245", "172.18.0.246", "172.18.0.247",
		"172.18.0.248", "172.18.0.249", "172.18.0.250",
	}, nil
}

// findBestIP finds the best IP for the requested ports and manages port assignment
func (r *InstanceReconciler) findBestIPAndAssignPorts(ctx context.Context, instance *clv1alpha2.Instance, usedPortsByIP map[string]map[int]bool) (string, []clv1alpha2.ServicePortMapping, error) {
	log := ctrl.LoggerFrom(ctx)

	// 1. Get available IP pool from MetalLB
	ipPool, err := r.getMetalLBIPPool(ctx)
	if err != nil {
		return "", nil, err
	}

	// Create a local copy of used ports for simulation
	simulatedUsedPorts := make(map[string]map[int]bool)
	for ip, ports := range usedPortsByIP {
		simulatedUsedPorts[ip] = make(map[int]bool)
		for port := range ports {
			simulatedUsedPorts[ip][port] = true
		}
	}

	// 2. Split service ports into specified and auto ports
	var specifiedPorts, autoPorts []clv1alpha2.ServicePortMapping
	for _, svcPort := range instance.Spec.PublicExposure.Services {
		if svcPort.Port != 0 {
			specifiedPorts = append(specifiedPorts, svcPort)
		} else {
			autoPorts = append(autoPorts, svcPort)
		}
	}

	// Choose best IP considering specified ports first
	var bestIP string
	var allAssignedPorts []clv1alpha2.ServicePortMapping

	// 3. Examine each available IP
	for _, ip := range ipPool {
		// Initialize port map if it doesn't exist
		if simulatedUsedPorts[ip] == nil {
			simulatedUsedPorts[ip] = make(map[int]bool)
		}

		// Flag to track if this IP is compatible with all specified ports
		isIPCompatible := true

		// 4. First check if all specified ports can be assigned
		var tempAssignedSpecific []clv1alpha2.ServicePortMapping
		for _, port := range specifiedPorts {
			// Check if requested port is already in use
			if simulatedUsedPorts[ip][int(port.Port)] {
				isIPCompatible = false
				log.Info("Specified port already in use", "ip", ip, "port", port.Port)
				break
			}

			// Simulate port assignment
			simulatedUsedPorts[ip][int(port.Port)] = true
			assignedPort := clv1alpha2.ServicePortMapping{
				Name:         port.Name,
				TargetPort:   port.TargetPort,
				Port:         port.Port,
				AssignedPort: port.Port, // For specified ports, AssignedPort = Port
			}
			tempAssignedSpecific = append(tempAssignedSpecific, assignedPort)
		}

		// If not compatible with specified ports, try next IP
		if !isIPCompatible {
			continue
		}

		// 5. Now assign automatic ports
		var tempAssignedAuto []clv1alpha2.ServicePortMapping
		allAutoPortsAssignable := true

		for _, port := range autoPorts {
			// Find free port in range 30000-32767
			var assignedPort int32
			for potentialPort := basePort; potentialPort <= 32767; potentialPort++ {
				// Verify port isn't already used or requested by other ServiceRequests
				if !simulatedUsedPorts[ip][potentialPort] {
					assignedPort = int32(potentialPort)
					simulatedUsedPorts[ip][potentialPort] = true
					break
				}
			}

			if assignedPort == 0 {
				// Couldn't find free port
				allAutoPortsAssignable = false
				log.Info("Cannot find free port for automatic assignment", "ip", ip)
				break
			}

			tempAssignedAuto = append(tempAssignedAuto, clv1alpha2.ServicePortMapping{
				Name:         port.Name,
				TargetPort:   port.TargetPort,
				Port:         0, // Original port is 0 (automatic)
				AssignedPort: assignedPort,
			})
		}

		// 6. If all ports assignable, this is the best IP
		if allAutoPortsAssignable {
			bestIP = ip
			allAssignedPorts = append(tempAssignedSpecific, tempAssignedAuto...)
			log.Info("Found compatible IP", "ip", bestIP)
			break
		}
	}

	if bestIP == "" {
		return "", nil, fmt.Errorf("no available IP can support all requested ports")
	}

	// Update the real map of used ports
	for _, port := range allAssignedPorts {
		if usedPortsByIP[bestIP] == nil {
			usedPortsByIP[bestIP] = make(map[int]bool)
		}
		usedPortsByIP[bestIP][int(port.AssignedPort)] = true
		log.Info("Port registered as in use", "ip", bestIP, "port", port.AssignedPort)
	}

	return bestIP, allAssignedPorts, nil
}

// updateUsedPortsByIP updates the map of ports in use for each IP
func (r *InstanceReconciler) updateUsedPortsByIP(ctx context.Context, namespace string) (map[string]map[int]bool, error) {
	usedPortsByIP := make(map[string]map[int]bool)
	logger := log.FromContext(ctx)

	// Get all LoadBalancer services
	svcList := &v1.ServiceList{}
	if err := r.List(ctx, svcList, client.InNamespace(namespace)); err != nil {
		return nil, err
	}

	for _, svc := range svcList.Items {
		if svc.Spec.Type != v1.ServiceTypeLoadBalancer {
			continue
		}

		// Get the assigned external IP
		var externalIP string
		if len(svc.Status.LoadBalancer.Ingress) > 0 {
			externalIP = svc.Status.LoadBalancer.Ingress[0].IP
		} else {
			// If the service doesn't have an IP yet, try to find it in annotations
			if specIP, ok := svc.Annotations["metallb.universe.tf/loadBalancerIPs"]; ok {
				externalIP = specIP
			} else {
				continue // Cannot determine the IP
			}
		}

		// Initialize the map for this IP if it doesn't exist
		if _, exists := usedPortsByIP[externalIP]; !exists {
			usedPortsByIP[externalIP] = make(map[int]bool)
		}

		// Register used ports
		for _, port := range svc.Spec.Ports {
			usedPortsByIP[externalIP][int(port.Port)] = true
			logger.Info("Port registered as in use", "ip", externalIP, "port", port.Port)
		}
	}

	return usedPortsByIP, nil
}
