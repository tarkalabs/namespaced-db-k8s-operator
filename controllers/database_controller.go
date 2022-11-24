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
	"github.com/go-logr/logr"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"time"

	dbv1alpha1 "github.com/tarkalabs/namespaced-db-operator/api/v1alpha1"

	_ "github.com/lib/pq"
)

const databaseFinalizer = "db.tarkalabs.com/finalizer"

// DatabaseReconciler reconciles a Database object
type DatabaseReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=db.tarkalabs.com,resources=databases,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=db.tarkalabs.com,resources=databases/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=db.tarkalabs.com,resources=databases/finalizers,verbs=update
//+kubebuilder:rbac:groups=db.tarkalabs.com,resources=databaseinstances,verbs=get;list
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;update;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Database object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.12.2/pkg/reconcile
func (r *DatabaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	var err error
	const requeueAfterDuration = "2s"

	var database dbv1alpha1.Database
	if err = r.Get(ctx, req.NamespacedName, &database); err != nil {
		log.Error(err, "Unable to fetch Database")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	databaseSecret := v1.Secret{}
	if err = r.Get(ctx, client.ObjectKey{Name: database.Spec.Secret, Namespace: req.Namespace}, &databaseSecret); err != nil {
		log.Error(err, "Unable to fetch Database Secret")
		return ctrl.Result{}, err
	}

	var databaseInstance dbv1alpha1.DatabaseInstance
	if err = r.Get(ctx, client.ObjectKey{Name: database.Spec.InstanceName, Namespace: req.Namespace}, &databaseInstance); err != nil {
		log.Error(err, "Unable to fetch DatabaseInstance")
		return ctrl.Result{}, err
	}

	databaseInstanceSecret := v1.Secret{}
	if err = r.Get(ctx, client.ObjectKey{Name: databaseInstance.Spec.AuthSecret, Namespace: req.Namespace}, &databaseInstanceSecret); err != nil {
		log.Error(err, "Unable to fetch DatabaseInstance Secret")
		return ctrl.Result{}, err
	}

	psqlInfo := func(dbname string) string {
		return fmt.Sprintf("host=%s port=%s user=%s "+"password=%s dbname=%s sslmode=disable",
			databaseInstance.Spec.Host, databaseInstance.Spec.Port, string(databaseInstanceSecret.Data["username"]),
			string(databaseInstanceSecret.Data["password"]), dbname)
	}
	var db *sql.DB
	db, err = sql.Open("postgres", psqlInfo(databaseInstance.Spec.Name))
	if err != nil {
		log.Error(err, fmt.Sprintf("sql.Open call failed for: %s", databaseInstance.Spec.Name))
		return ctrl.Result{}, err
	}

	var dbPresent bool
	if err = db.QueryRow("SELECT FROM pg_database WHERE datname = $1", database.Spec.Name).Scan(); err != sql.ErrNoRows {
		dbPresent = true
	}
	if !dbPresent {
		db.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE %s", database.Spec.Name))
		log.Info(fmt.Sprintf("Database %s has been successfully created", database.Spec.Name))
	}

	var userPresent bool
	if err = db.QueryRow("SELECT FROM pg_roles WHERE rolname = $1", databaseSecret.Data["username"]).Scan(); err != sql.ErrNoRows {
		userPresent = true
	}
	db.Close()

	if !userPresent {
		db, err = sql.Open("postgres", psqlInfo(database.Spec.Name))
		if err != nil {
			log.Error(err, fmt.Sprintf("sql.Open call failed for: %s", database.Spec.Name))
			return ctrl.Result{}, err
		}
		defer db.Close()

		var tx *sql.Tx
		tx, err = db.BeginTx(ctx, nil)
		if err != nil {
			log.Error(err, fmt.Sprintf("Not able to begin db transaction for: %s", database.Spec.Name))
			return ctrl.Result{}, err
		}
		defer tx.Rollback()
		tx.ExecContext(ctx, fmt.Sprintf("CREATE USER %s WITH PASSWORD '%s'", databaseSecret.Data["username"], databaseSecret.Data["password"]))
		tx.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA %s", databaseSecret.Data["username"]))
		tx.ExecContext(ctx, fmt.Sprintf("REVOKE CONNECT ON DATABASE %s FROM public", database.Spec.Name))
		tx.ExecContext(ctx, "REVOKE ALL ON SCHEMA public FROM public")
		tx.ExecContext(ctx, "REVOKE ALL ON ALL TABLES IN SCHEMA public FROM public")
		tx.ExecContext(ctx, fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO %s", database.Spec.Name, databaseSecret.Data["username"]))
		tx.ExecContext(ctx, fmt.Sprintf("GRANT ALL ON SCHEMA %s TO %s", databaseSecret.Data["username"], databaseSecret.Data["username"]))
		tx.ExecContext(ctx, fmt.Sprintf("GRANT ALL ON ALL TABLES IN SCHEMA %s TO %s", databaseSecret.Data["username"], databaseSecret.Data["username"]))
		tx.ExecContext(ctx, fmt.Sprintf("GRANT ALL ON ALL SEQUENCES IN SCHEMA %s TO %s", databaseSecret.Data["username"], databaseSecret.Data["username"]))
		tx.ExecContext(ctx, fmt.Sprintf("GRANT ALL ON ALL FUNCTIONS IN SCHEMA %s TO %s", databaseSecret.Data["username"], databaseSecret.Data["username"]))
		tx.ExecContext(ctx, fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR USER %s IN SCHEMA %s GRANT ALL ON TABLES TO %s", databaseSecret.Data["username"], databaseSecret.Data["username"], databaseSecret.Data["username"]))
		tx.ExecContext(ctx, fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR USER %s IN SCHEMA %s GRANT ALL ON SEQUENCES TO %s", databaseSecret.Data["username"], databaseSecret.Data["username"], databaseSecret.Data["username"]))
		tx.ExecContext(ctx, fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR USER %s IN SCHEMA %s GRANT ALL ON FUNCTIONS TO %s", databaseSecret.Data["username"], databaseSecret.Data["username"], databaseSecret.Data["username"]))

		if err = tx.Commit(); err != nil {
			log.Error(err, fmt.Sprintf("Not able to commit db transaction for: %s", database.Spec.Name))
			return ctrl.Result{}, err
		}
		log.Info(fmt.Sprintf("User %s has been successfully created", databaseSecret.Data["username"]))
		db.Close()
	}

	if isDatabaseMarkedToBeDeleted := database.GetDeletionTimestamp(); isDatabaseMarkedToBeDeleted != nil {
		if controllerutil.ContainsFinalizer(&database, databaseFinalizer) {
			if err := r.finalizeDatabase(log, db, database.Spec.Name, databaseInstance.Spec.Name, string(databaseSecret.Data["username"]), ctx, psqlInfo); err != nil {
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(&database, databaseFinalizer)
			err := r.Update(ctx, &database)
			if err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	if !controllerutil.ContainsFinalizer(&database, databaseFinalizer) {
		controllerutil.AddFinalizer(&database, databaseFinalizer)
		err = r.Update(ctx, &database)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	//https://github.com/kubernetes-sigs/controller-runtime/issues/617
	duration, err := time.ParseDuration(requeueAfterDuration)
	return ctrl.Result{RequeueAfter: duration}, nil
}

func (r *DatabaseReconciler) finalizeDatabase(reqLogger logr.Logger, db *sql.DB, database string, databaseInstance string, databaseUser string, ctx context.Context, psqlInfo func(dbname string) string) error {
	var err error
	db, err = sql.Open("postgres", psqlInfo(database))
	if err != nil {
		reqLogger.Error(err, fmt.Sprintf("sql.Open call failed for: %s", database))
		return err
	}
	db.ExecContext(ctx, fmt.Sprintf("DROP OWNED BY %s", databaseUser))
	db.ExecContext(ctx, fmt.Sprintf("DROP USER %s", databaseUser))
	reqLogger.Info(fmt.Sprintf("Successfully dropped user: %s", databaseUser))
	db.Close()
	db, err = sql.Open("postgres", psqlInfo(databaseInstance))
	if err != nil {
		reqLogger.Error(err, fmt.Sprintf("sql.Open call failed for: %s", databaseInstance))
		return err
	}
	db.ExecContext(ctx, fmt.Sprintf("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s'", database))
	db.ExecContext(ctx, fmt.Sprintf("DROP DATABASE %s", database))
	reqLogger.Info(fmt.Sprintf("Successfully dropped database: %s", database))
	db.Close()
	reqLogger.Info("Successfully finalized database")
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DatabaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dbv1alpha1.Database{}).
		Complete(r)
}
