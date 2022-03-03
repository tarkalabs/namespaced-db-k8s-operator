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

	"github.com/go-logr/logr"
	_ "github.com/go-sql-driver/mysql"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dbv1alpha1 "github.com/tarkalabs/namespaced-db-k8s-operator/api/v1alpha1"
)

const passwordChars = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

const nsDbCredsOwnerLabelName = "namespaceddbcreds-owner-name"

const crdFinalizer = "db.tarkalabs.com/finalizer"

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
func (r *NamespacedDBCredsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info(fmt.Sprintf("Reconciling started at %s", time.Now().Format("2006-01-02 15:04:05")))

	// Fetch the NamespacedDBCreds instance
	instance := &dbv1alpha1.NamespacedDBCreds{}
	err := r.Get(ctx, req.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("NamespacedDBCreds object not found. Ignoring since object must be deleted.")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Unable to fetch NamespacedDBCreds "+instance.Name)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if _, err := checkAndUpdateFinalizers(*r, ctx, instance, log); err != nil {
		log.Error(err, "Unable to update finalizers")
		return ctrl.Result{}, err
	}

	// Only works on active objects
	if instance.GetDeletionTimestamp() == nil {
		if _, err := checkAndUpdateSecrets(*r, ctx, instance, log); err != nil {
			log.Error(err, "Unable to update secrets in all namespaces")
			return ctrl.Result{RequeueAfter: time.Minute}, err
		}
	}

	log.Info(fmt.Sprintf("Reconciling ended at %s", time.Now().Format("2006-01-02 15:04:05")))
	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

func checkAndUpdateFinalizers(r NamespacedDBCredsReconciler, ctx context.Context, instance *dbv1alpha1.NamespacedDBCreds, log logr.Logger) (ctrl.Result, error) {
	if controllerutil.ContainsFinalizer(instance, crdFinalizer) {
		// Check if the instance is marked to be deleted, which is
		// indicated by the deletion timestamp being set.
		if instance.GetDeletionTimestamp() != nil {
			// Run finalizer logic for this CRD
			if err := r.finalizeNamespacedDbCredDelete(instance, log); err != nil {
				return ctrl.Result{}, err
			}
			// Remove finalizer if work is done
			controllerutil.RemoveFinalizer(instance, crdFinalizer)
			err := r.Update(ctx, instance)
			if err != nil {
				return ctrl.Result{}, err
			}
		}
	} else if instance.GetDeletionTimestamp() == nil {
		// Add finalizer if it doesn't exist and the object is not being deleted
		controllerutil.AddFinalizer(instance, crdFinalizer)
		err := r.Update(ctx, instance)
		if err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func (r *NamespacedDBCredsReconciler) finalizeNamespacedDbCredDelete(instance *dbv1alpha1.NamespacedDBCreds, log logr.Logger) error {
	log.Info("Deleting all the secrets across all namespaces")
	namespaces := &corev1.NamespaceList{}
	err := r.List(context.TODO(), namespaces)
	if err != nil {
		log.Error(err, "Unable to fetch Namespaces")
		return err
	} else {
		for _, namespace := range namespaces.Items {
			existingSecret := &corev1.Secret{}
			err = r.Get(context.TODO(), client.ObjectKey{Name: instance.Name, Namespace: namespace.Name}, existingSecret)
			if err != nil && errors.IsNotFound(err) {
				continue
			} else if err != nil {
				log.Error(err, "Unable to fetch Namespace secret: "+namespace.Name)
				return err
			} else {
				err = r.Delete(context.TODO(), existingSecret)
				if err != nil {
					log.Error(err, "Unable to delete Secret in Namespace: "+namespace.Name)
					return err
				} else {
					log.Info("Secret deleted in Namespace: " + namespace.Name)
				}
			}
		}
	}
	log.Info("Deleted all secrets across all namespaces")
	return nil
}

func checkAndUpdateSecrets(r NamespacedDBCredsReconciler, ctx context.Context, instance *dbv1alpha1.NamespacedDBCreds, log logr.Logger) (ctrl.Result, error) {
	db, err := sql.Open("mysql", getDataSourceName(instance))
	if err != nil {
		log.Error(err, "Unable to connect to database of "+instance.Name)
		return ctrl.Result{}, err
	}
	defer db.Close()

	// Fetch the Namespaces
	namespaces := &corev1.NamespaceList{}
	err = r.List(ctx, namespaces)
	if err != nil {
		log.Error(err, "Unable to fetch Namespaces")
		return ctrl.Result{}, err
	} else {
		// Create db secret for each Namespace
		for _, namespace := range namespaces.Items {
			existingSecret := &corev1.Secret{}

			err = r.Get(ctx, client.ObjectKey{Name: instance.Name, Namespace: namespace.Name}, existingSecret)

			if err != nil && errors.IsNotFound(err) {
				dbPassword := generateNewPassword(16)
				if !createDatabase(ctx, db, namespace.Name, dbPassword, true) {
					return ctrl.Result{}, err
				}

				secret := getSecret(instance, &namespace, nil, dbPassword)
				err = r.Create(ctx, &secret)
				if err != nil {
					log.Error(err, "Unable to create Secret in Namespace: "+namespace.Name)
					return ctrl.Result{}, err
				} else {
					// Should set controller & owner references but our usecase is to create secret in all namespaces
					// If we set references, we will get an error saying `cross-namespace references are disallowed`.
					log.Info("Secret created in Namespace: " + namespace.Name)
				}
			} else if err != nil {
				log.Error(err, "Unable to fetch Namespace secret: "+namespace.Name)
				return ctrl.Result{}, err
			} else {
				dbPort, _ := strconv.Atoi(string(existingSecret.Data["port"]))
				if (instance.Spec.DBHost != string(existingSecret.Data["host"])) || (instance.Spec.DBPort != dbPort) {
					secret := getSecret(instance, &namespace, existingSecret, "")

					if !createDatabase(ctx, db, namespace.Name, generateNewPassword(16), true) {
						log.Error(err, "Unable to create database for Namespace: "+namespace.Name)
						return ctrl.Result{}, err
					}

					err = r.Update(ctx, &secret)
					if err != nil {
						log.Error(err, "Unable to update Secret in Namespace: "+namespace.Name)
						return ctrl.Result{}, err
					} else {
						log.Info("Secret updated in Namespace: " + namespace.Name)
					}
				} else {
					if !checkDatabaseAndUser(db, namespace.Name) {
						log.Error(nil, "Database or User got deleted for Namespace: "+namespace.Name)
						if !createDatabase(ctx, db, namespace.Name, string(existingSecret.Data["password"]), false) {
							log.Error(err, "Unable to create database for Namespace: "+namespace.Name)
							return ctrl.Result{}, err
						} else {
							log.Info("Database & User are created for Namespace: " + namespace.Name)
						}
					}
				}
			}
		}
	}
	return ctrl.Result{}, nil
}

func getSecret(instance *dbv1alpha1.NamespacedDBCreds, namespace *corev1.Namespace, existingSecret *corev1.Secret, passsword string) corev1.Secret {
	metadata := metav1.ObjectMeta{
		Name:      instance.Name,
		Namespace: namespace.Name,
		Labels:    map[string]string{nsDbCredsOwnerLabelName: instance.Name},
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

func createDatabase(ctx context.Context, db *sql.DB, nsName string, password string, dropDatabase bool) bool {
	log := log.FromContext(ctx)
	dbTxnCtx, cancelfunc := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelfunc()

	tx, err := db.BeginTx(dbTxnCtx, nil)
	if err != nil {
		log.Error(err, "Unable to begin db transaction")
		return false
	}

	if dropDatabase {
		log.Info("Dropping database: " + nsName)
		tx.ExecContext(dbTxnCtx, fmt.Sprintf("DROP DATABASE `%s`", nsName))
	}
	tx.ExecContext(dbTxnCtx, fmt.Sprintf("DROP USER `%s`", nsName))

	_, err = tx.ExecContext(dbTxnCtx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", nsName))
	if err != nil {
		log.Error(err, "Unable to create database: "+nsName)
		tx.Rollback()
		return false
	}

	_, err = tx.ExecContext(dbTxnCtx, fmt.Sprintf("CREATE USER `%s`@`%%` IDENTIFIED BY '%s'", nsName, password))
	if err != nil {
		log.Error(err, "Unable to create user: "+nsName)
		tx.Rollback()
		return false
	}

	_, err = tx.ExecContext(dbTxnCtx, fmt.Sprintf("GRANT ALL PRIVILEGES ON `%[1]s`.* TO `%[1]s`@`%%`", nsName))
	if err != nil {
		log.Error(err, "Unable to grant privileges for user to database: "+nsName)
		tx.Rollback()
		return false
	}

	_, err = tx.ExecContext(dbTxnCtx, "FLUSH PRIVILEGES")
	if err != nil {
		log.Error(err, "Unable to flush privileges")
		tx.Rollback()
		return false
	}

	err = tx.Commit()
	if err != nil {
		log.Error(err, "Unable to commit db transaction")
	} else {
		log.Info("Schema & user configured for namespace: " + nsName)
	}
	return err == nil
}

func getDataSourceName(instance *dbv1alpha1.NamespacedDBCreds) string {
	return fmt.Sprintf("%s:%s@tcp(%s:%v)/", instance.Spec.DBAdminUserName, instance.Spec.DBAdminPassword, instance.Spec.DBHost, instance.Spec.DBPort)
}

func generateNewPassword(size int) string {
	buf := make([]byte, size)
	for i := 0; i < size; i++ {
		buf[i] = passwordChars[rand.Intn(len(passwordChars))]
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
