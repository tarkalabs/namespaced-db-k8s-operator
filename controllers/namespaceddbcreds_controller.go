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
	"math/rand"
	"strconv"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dbv1alpha1 "github.com/tarkalabs/namespaced-db-k8s-operator/api/v1alpha1"
)

const PASSWORD_CHARS = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

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
	log := log.FromContext(ctx)

	log.Info(fmt.Sprintf("Reconciling started at %s", time.Now().Format("2006-01-02 15:04:05")))

	// Fetch the NamespacedDBCreds instance
	instance := &dbv1alpha1.NamespacedDBCreds{}
	err := r.Get(ctx, req.NamespacedName, instance)

	if err != nil {
		log.Error(err, "Unable to fetch NamespacedDBCreds "+instance.Name)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	db, err := sql.Open("mysql", getDataSourceName(instance))
	if err != nil {
		log.Error(err, "Unable to connect to database of "+instance.Name)
		return ctrl.Result{RequeueAfter: time.Minute}, err
	}
	defer db.Close()

	// Fetch the Namespaces
	namespaces := &corev1.NamespaceList{}
	err = r.List(ctx, namespaces)
	if err != nil {
		log.Error(err, "Unable to fetch Namespaces")
		return ctrl.Result{RequeueAfter: time.Minute}, err
	} else {
		// Create db secret for each Namespace
		for _, namespace := range namespaces.Items {
			existingSecret := &corev1.Secret{}

			err = r.Get(ctx, client.ObjectKey{Name: instance.Name, Namespace: namespace.Name}, existingSecret)

			if err != nil && errors.IsNotFound(err) {
				dbPassword := generateNewPassword(16)
				if !createDatabase(db, namespace.Name, dbPassword) {
					return ctrl.Result{RequeueAfter: time.Minute}, err
				}

				secret := getSecret(instance, &namespace, nil, dbPassword)
				err = r.Create(ctx, &secret)
				if err != nil {
					log.Error(err, "Unable to create Secret in Namespace: "+namespace.Name)
					return ctrl.Result{RequeueAfter: time.Minute}, err
				} else {
					log.Info("Secret created in Namespace: " + namespace.Name)
				}
			} else if err != nil {
				log.Error(err, "Unable to fetch Namespace secret: "+namespace.Name)
				return ctrl.Result{RequeueAfter: time.Minute}, err
			} else {
				dbPort, _ := strconv.Atoi(string(existingSecret.Data["port"]))
				if (instance.Spec.DBHost != string(existingSecret.Data["host"])) || (instance.Spec.DBPort != dbPort) {
					secret := getSecret(instance, &namespace, existingSecret, "")

					if !createDatabase(db, namespace.Name, generateNewPassword(16)) {
						log.Error(err, "Unable to create database for Namespace: "+namespace.Name)
						return ctrl.Result{RequeueAfter: time.Minute}, err
					}

					err = r.Update(ctx, &secret)
					if err != nil {
						log.Error(err, "Unable to update Secret in Namespace: "+namespace.Name)
						return ctrl.Result{RequeueAfter: time.Minute}, err
					} else {
						log.Info("Secret updated in Namespace: " + namespace.Name)
					}
				} else {
					if !checkDatabaseAndUser(db, namespace.Name) {
						log.Error(nil, "Database or User got deleted for Namespace: "+namespace.Name)
						if !createDatabase(db, namespace.Name, string(existingSecret.Data["password"])) {
							log.Error(err, "Unable to create database for Namespace: "+namespace.Name)
							return ctrl.Result{RequeueAfter: time.Minute}, err
						} else {
							log.Info("Database & User are created for Namespace: " + namespace.Name)
						}
					}
				}
			}
		}
	}

	log.Info(fmt.Sprintf("Reconciling ended at %s", time.Now().Format("2006-01-02 15:04:05")))
	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

func getSecret(instance *dbv1alpha1.NamespacedDBCreds, namespace *corev1.Namespace, existingSecret *corev1.Secret, passsword string) corev1.Secret {
	metadata := metav1.ObjectMeta{
		Name:      instance.Name,
		Namespace: namespace.Name,
	}
	if existingSecret != nil && passsword == "" {
		metadata = existingSecret.ObjectMeta
		passsword = string(existingSecret.Data["password"])
	}
	return corev1.Secret{
		ObjectMeta: metadata,
		Data: map[string][]byte{
			"host":     []byte(instance.Spec.DBHost),
			"port":     []byte(fmt.Sprintf("%v", instance.Spec.DBPort)),
			"username": []byte(namespace.Name),
			"password": []byte(passsword),
		},
	}
}

func checkDatabaseAndUser(db *sql.DB, namespace string) bool {
	err := db.QueryRow("SELECT SCHEMA_NAME FROM INFORMATION_SCHEMA.SCHEMATA WHERE SCHEMA_NAME = ?", namespace).Scan(&namespace)
	if err != nil {
		return false
	}
	err = db.QueryRow("SELECT USER FROM mysql.user WHERE USER = ?", namespace).Scan(&namespace)
	return err == nil
}

func createDatabase(db *sql.DB, nsName string, password string) bool {
	ctx, cancelfunc := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelfunc()

	log := log.FromContext(ctx)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Error(err, "Unable to begin db transaction")
		return false
	}

	// Dropping database and user if exists
	tx.ExecContext(ctx, fmt.Sprintf("DROP DATABASE `%s`", nsName))
	tx.ExecContext(ctx, fmt.Sprintf("DROP USER `%s`", nsName))

	_, err = tx.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE `%s`", nsName))
	if err != nil {
		log.Error(err, "Unable to create database: "+nsName)
		tx.Rollback()
		return false
	}

	_, err = tx.ExecContext(ctx, fmt.Sprintf("CREATE USER `%s`@`%%` IDENTIFIED BY '%s'", nsName, password))
	if err != nil {
		log.Error(err, "Unable to create user: "+nsName)
		tx.Rollback()
		return false
	}

	_, err = tx.ExecContext(ctx, fmt.Sprintf("GRANT ALL PRIVILEGES ON `%[1]s`.* TO `%[1]s`@`%%`", nsName))
	if err != nil {
		log.Error(err, "Unable to grant privileges for user to database: "+nsName)
		tx.Rollback()
		return false
	}

	_, err = tx.ExecContext(ctx, "FLUSH PRIVILEGES")
	if err != nil {
		log.Error(err, "Unable to flush privileges")
		tx.Rollback()
		return false
	}

	err = tx.Commit()
	if err != nil {
		log.Error(err, "Unable to commit db transaction")
		return false
	} else {
		log.Info("Schema & user configured for namespace: " + nsName)
		return true
	}
}

func getDataSourceName(instance *dbv1alpha1.NamespacedDBCreds) string {
	return fmt.Sprintf("%s:%s@tcp(%s:%v)/?multiStatements=true", instance.Spec.DBAdminUserName, instance.Spec.DBAdminPassword, instance.Spec.DBHost, instance.Spec.DBPort)
}

func generateNewPassword(size int) string {
	buf := make([]byte, size)
	for i := 0; i < size; i++ {
		buf[i] = PASSWORD_CHARS[rand.Intn(len(PASSWORD_CHARS))]
	}
	return string(buf)
}

// SetupWithManager sets up the controller with the Manager.
func (r *NamespacedDBCredsReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dbv1alpha1.NamespacedDBCreds{}).
		Owns(&corev1.Secret{}).
		Complete(r)
}
