/*
Copyright 2022.

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

package controllers

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dbv1alpha1 "github.com/tarkalabs/namespaced-db-k8s-operator/api/v1alpha1"
)

// NamespacedDBCredsReconciler reconciles a NamespacedDBCreds object
type NamespacedDBCredsReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=db.tarkalabs.com,resources=namespaceddbcreds,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=db.tarkalabs.com,resources=namespaceddbcreds/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=db.tarkalabs.com,resources=namespaceddbcreds/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the NamespacedDBCreds object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.9.2/pkg/reconcile
func (r *NamespacedDBCredsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx).WithValues("namespaceddbcreds", req.NamespacedName)

	// Fetch the NamespacedDBCreds instance
	instance := &dbv1alpha1.NamespacedDBCreds{}
	err := r.Get(ctx, req.NamespacedName, instance)

	if err != nil {
		log.Error(err, "unable to fetch NamespacedDBCreds "+instance.Name)
		return ctrl.Result{RequeueAfter: time.Minute}, client.IgnoreNotFound(err)
	}

	// Fetch the Namespaces
	namespaces := &corev1.NamespaceList{}
	err = r.List(ctx, namespaces)
	if err != nil {
		log.Error(err, "unable to fetch Namespaces")
		return ctrl.Result{RequeueAfter: time.Minute}, err
	} else {
		// Create db secret for each Namespace
		for _, namespace := range namespaces.Items {
			exisingSecret := &corev1.Secret{}

			err = r.Get(ctx, client.ObjectKey{Name: instance.Name, Namespace: namespace.Name}, exisingSecret)

			if err != nil && errors.IsNotFound(err) {
				secret := generateSecret(instance, &namespace, nil)
				err = r.Create(ctx, &secret)
				if err != nil {
					log.Error(err, "unable to create Secret in Namespace: "+namespace.Name)
					return ctrl.Result{RequeueAfter: time.Minute}, err
				} else {
					log.Info("Secret created in Namespace: " + namespace.Name)
				}
			} else if err != nil {
				log.Error(err, "unable to fetch Namespace secret: "+namespace.Name)
				return ctrl.Result{RequeueAfter: time.Minute}, err
			} else {
				secret := generateSecret(instance, &namespace, exisingSecret)
				err = r.Update(ctx, &secret)
				if err != nil {
					log.Error(err, "unable to update Secret in Namespace: "+namespace.Name)
					return ctrl.Result{RequeueAfter: time.Minute}, err
				} else {
					log.Info("Secret updated in Namespace: " + namespace.Name)
				}
			}
		}
	}

	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

func generateSecret(instance *dbv1alpha1.NamespacedDBCreds, namespace *corev1.Namespace, secret *corev1.Secret) corev1.Secret {
	metadata := metav1.ObjectMeta{
		Name:      instance.Name,
		Namespace: namespace.Name,
	}
	if secret != nil {
		metadata = secret.ObjectMeta
	}
	return corev1.Secret{
		ObjectMeta: metadata,
		Data: map[string][]byte{
			"host":     []byte(instance.Spec.DBHost),
			"port":     []byte(fmt.Sprintf("%v", instance.Spec.DBPort)),
			"username": []byte(instance.Spec.DBAdminUserName),
			"password": []byte(instance.Spec.DBAdminPassword),
		},
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *NamespacedDBCredsReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dbv1alpha1.NamespacedDBCreds{}).
		Owns(&corev1.Secret{}).
		Complete(r)
}
