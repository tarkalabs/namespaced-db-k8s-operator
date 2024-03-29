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
	"database/sql"
	"fmt"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	dbv1alpha1 "github.com/tarkalabs/namespaced-db-operator/api/v1alpha1"

	_ "github.com/lib/pq"
)

// DatabaseInstanceReconciler reconciles a DatabaseInstance object
type DatabaseInstanceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=db.tarkalabs.com,resources=databaseinstances,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=db.tarkalabs.com,resources=databaseinstances/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=db.tarkalabs.com,resources=databaseinstances/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the DatabaseInstance object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.12.2/pkg/reconcile
func (r *DatabaseInstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	var err error

	var databaseInstance dbv1alpha1.DatabaseInstance
	if err = r.Get(ctx, req.NamespacedName, &databaseInstance); err != nil {
		log.Error(err, "Unable to fetch DatabaseInstance")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	secret := v1.Secret{}
	if err = r.Get(ctx, client.ObjectKey{Name: databaseInstance.Spec.AuthSecret, Namespace: req.Namespace}, &secret); err != nil {
		log.Error(err, "Unable to fetch DatabaseInstance Secret")
		return ctrl.Result{}, err
	}

	psqlInfo := fmt.Sprintf("host=%s port=%s user=%s "+"password=%s dbname=%s sslmode=disable",
		databaseInstance.Spec.Host, databaseInstance.Spec.Port, string(secret.Data["username"]),
		string(secret.Data["password"]), databaseInstance.Spec.Name)
	var db *sql.DB
	db, err = sql.Open("postgres", psqlInfo)
	if err != nil {
		log.Error(err, fmt.Sprintf("sql.Open call failed for: %s", databaseInstance.Name))
		return ctrl.Result{}, err
	}
	defer db.Close()

	if err = db.Ping(); err != nil {
		log.Error(err, fmt.Sprintf("Ping failed for: %s", databaseInstance.Name))
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DatabaseInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dbv1alpha1.DatabaseInstance{}).
		Complete(r)
}
