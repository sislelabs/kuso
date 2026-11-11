package kube

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// namespaceFullyScoped reports whether ns hosts at least one project and
// every project there has a scoped backup key whose rendered Secret
// exists in ns (so the chart's lookup picks it over the shared one).
func (c *Client) namespaceFullyScoped(ctx context.Context, ns string) (bool, error) {
	if c.Dynamic == nil {
		return false, nil
	}
	store, err := c.Clientset.CoreV1().Secrets(ServerSANamespace).Get(ctx, BackupScopedSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("kube: read %s: %w", BackupScopedSecretName, err)
	}
	projects, err := c.ListKusoProjects(ctx, ServerSANamespace)
	if err != nil {
		return false, fmt.Errorf("kube: list projects: %w", err)
	}
	var n int
	for i := range projects {
		pns := projects[i].Spec.Namespace
		if pns == "" {
			pns = ServerSANamespace
		}
		if pns != ns {
			continue
		}
		ak, sk := scopedKeys(projects[i].Name)
		if len(store.Data[ak]) == 0 || len(store.Data[sk]) == 0 {
			return false, nil
		}
		name := ProjectBackupSecretName(projects[i].Name)
		if _, err := c.Clientset.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, fmt.Errorf("kube: read %s/%s: %w", ns, name, err)
		}
		n++
	}
	return n > 0, nil
}

// dropSharedBackupCopy deletes a kuso-written copy of the shared backup
// Secret from ns once nothing that runs there still mounts it. A CronJob
// rendered before the project was scoped keeps the copy alive until the
// operator re-renders it onto the scoped Secret; a later heal removes it.
func (c *Client) dropSharedBackupCopy(ctx context.Context, ns string) error {
	secrets := c.Clientset.CoreV1().Secrets(ns)
	sec, err := secrets.Get(ctx, backupSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("kube: read %s/%s: %w", ns, backupSecretName, err)
	}
	// Both writers: the heal sweep and the restore handler's mirror.
	if mb := sec.Labels[ManagedByLabel]; mb != ManagedByValue && mb != "kuso-server" {
		return nil
	}
	inUse, err := c.backupSecretReferenced(ctx, ns)
	if err != nil || inUse {
		return err
	}
	if err := secrets.Delete(ctx, backupSecretName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("kube: delete %s/%s: %w", ns, backupSecretName, err)
	}
	return nil
}

// backupSecretReferenced reports whether a CronJob, or an unfinished Job,
// in ns reads the shared backup Secret.
func (c *Client) backupSecretReferenced(ctx context.Context, ns string) (bool, error) {
	cjs, err := c.Clientset.BatchV1().CronJobs(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, fmt.Errorf("kube: list cronjobs in %s: %w", ns, err)
	}
	for i := range cjs.Items {
		if podSpecReadsSecret(&cjs.Items[i].Spec.JobTemplate.Spec.Template.Spec, backupSecretName) {
			return true, nil
		}
	}
	jobs, err := c.Clientset.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, fmt.Errorf("kube: list jobs in %s: %w", ns, err)
	}
	for i := range jobs.Items {
		j := &jobs.Items[i]
		if j.Status.CompletionTime != nil || (j.Status.Failed > 0 && j.Status.Active == 0) {
			continue
		}
		if podSpecReadsSecret(&j.Spec.Template.Spec, backupSecretName) {
			return true, nil
		}
	}
	return false, nil
}

func podSpecReadsSecret(spec *corev1.PodSpec, name string) bool {
	for _, v := range spec.Volumes {
		if v.Secret != nil && v.Secret.SecretName == name {
			return true
		}
	}
	for _, ctrs := range [][]corev1.Container{spec.InitContainers, spec.Containers} {
		for i := range ctrs {
			for _, e := range ctrs[i].Env {
				if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil && e.ValueFrom.SecretKeyRef.Name == name {
					return true
				}
			}
			for _, ef := range ctrs[i].EnvFrom {
				if ef.SecretRef != nil && ef.SecretRef.Name == name {
					return true
				}
			}
		}
	}
	return false
}

// BackupSecretPresent reports whether a backup CronJob of project in ns
// has a storage Secret to read: the shared copy or the project's own.
func (c *Client) BackupSecretPresent(ctx context.Context, ns, project string) (bool, error) {
	names := []string{backupSecretName}
	if project != "" {
		names = append(names, ProjectBackupSecretName(project))
	}
	for _, n := range names {
		_, err := c.Clientset.CoreV1().Secrets(ns).Get(ctx, n, metav1.GetOptions{})
		if err == nil {
			return true, nil
		}
		if !apierrors.IsNotFound(err) {
			return false, err
		}
	}
	return false, nil
}

// ProjectBackupSecretIn returns the project's rendered backup Secret name
// when it exists in ns, "" when it does not.
func (c *Client) ProjectBackupSecretIn(ctx context.Context, ns, project string) (string, error) {
	name := ProjectBackupSecretName(project)
	_, err := c.Clientset.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("kube: read %s/%s: %w", ns, name, err)
	}
	return name, nil
}
