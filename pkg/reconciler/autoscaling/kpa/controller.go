/*
Copyright 2019 The Knative Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package kpa

import (
	"context"

	"github.com/knative/pkg/configmap"
	"github.com/knative/pkg/controller"
	"github.com/knative/serving/pkg/apis/autoscaling"
	"github.com/knative/serving/pkg/autoscaler"
	informers "github.com/knative/serving/pkg/client/informers/externalversions/autoscaling/v1alpha1"
	ninformers "github.com/knative/serving/pkg/client/informers/externalversions/networking/v1alpha1"
	"github.com/knative/serving/pkg/reconciler"
	"github.com/knative/serving/pkg/reconciler/autoscaling/config"
	"github.com/knative/serving/pkg/reconciler/autoscaling/kpa/resources"
	aresources "github.com/knative/serving/pkg/reconciler/autoscaling/resources"
	corev1informers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/tools/cache"
)

const controllerAgentName = "kpa-class-podautoscaler-controller"

// configStore is a minimized interface of the actual configStore.
type configStore interface {
	ToContext(ctx context.Context) context.Context
	WatchConfigs(w configmap.Watcher)
}

// NewController creates an autoscaling Controller.
func NewController(
	opts *reconciler.Options,
	paInformer informers.PodAutoscalerInformer,
	sksInformer ninformers.ServerlessServiceInformer,
	serviceInformer corev1informers.ServiceInformer,
	endpointsInformer corev1informers.EndpointsInformer,
	kpaDeciders resources.Deciders,
	metrics aresources.Metrics,
) *controller.Impl {
	c := &Reconciler{
		Base:            reconciler.NewBase(*opts, controllerAgentName),
		paLister:        paInformer.Lister(),
		sksLister:       sksInformer.Lister(),
		serviceLister:   serviceInformer.Lister(),
		endpointsLister: endpointsInformer.Lister(),
		kpaDeciders:     kpaDeciders,
		metrics:         metrics,
	}
	impl := controller.NewImpl(c, c.Logger, "KPA-Class Autoscaling")
	c.scaler = newScaler(opts, impl.EnqueueAfter)

	c.Logger.Info("Setting up KPA-Class event handlers")
	// Handle PodAutoscalers missing the class annotation for backward compatibility.
	onlyKpaClass := reconciler.AnnotationFilterFunc(autoscaling.ClassAnnotationKey, autoscaling.KPA, true)
	paHandler := cache.FilteringResourceEventHandler{
		FilterFunc: onlyKpaClass,
		Handler:    controller.HandleAll(impl.Enqueue),
	}
	paInformer.Informer().AddEventHandler(paHandler)

	endpointsInformer.Informer().AddEventHandler(
		controller.HandleAll(impl.EnqueueLabelOfNamespaceScopedResource("", autoscaling.KPALabelKey)))

	// Watch all the services that we have created.
	serviceInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: onlyKpaClass,
		Handler:    controller.HandleAll(impl.EnqueueControllerOf),
	})
	sksInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: onlyKpaClass,
		Handler:    controller.HandleAll(impl.EnqueueControllerOf),
	})

	// Have the Deciders enqueue the PAs whose decisions have changed.
	kpaDeciders.Watch(impl.EnqueueKey)

	c.Logger.Info("Setting up ConfigMap receivers")
	configsToResync := []interface{}{
		&autoscaler.Config{},
	}
	resync := configmap.TypeFilter(configsToResync...)(func(string, interface{}) {
		controller.SendGlobalUpdates(paInformer.Informer(), paHandler)
	})
	c.configStore = config.NewStore(c.Logger.Named("config-store"), resync)
	c.configStore.WatchConfigs(opts.ConfigMapWatcher)

	return impl
}
