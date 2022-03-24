package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	hyp "github.com/openshift/hypershift/api/v1alpha1"
	hyd "github.com/stolostron/hypershift-deployment-controller/api/v1alpha1"
	"github.com/stolostron/hypershift-deployment-controller/pkg/helper"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	workv1 "open-cluster-management.io/api/work/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type kindAndKey struct {
	schema.GroupVersionKind
	types.NamespacedName
}

// TestManifestWorkFlow tests when override is set to manifestwork, test if the manifestwork is created
// and referenece secret is put into manifestwork payload
func TestManifestWorkFlowPullSecret(t *testing.T) {
	client := initClient()
	ctx := context.Background()

	infraOut := getAWSInfrastructureOut()
	testHD := getHypershiftDeployment("default", "test1")
	testHD.Spec.Override = hyd.InfraConfigureWithManifest

	testHD.Spec.Infrastructure.Platform = &hyd.Platforms{AWS: &hyd.AWSPlatform{}}
	testHD.Spec.Credentials = &hyd.CredentialARNs{AWS: &hyd.AWSCredentials{}}
	testHD.Spec.InfraID = infraOut.InfraID
	ScaffoldAWSHostedClusterSpec(testHD, infraOut)
	ScaffoldAWSNodePoolSpec(testHD, infraOut)

	client.Create(ctx, testHD)
	defer client.Delete(ctx, testHD)

	// ensure the pull secret exist in cluster
	// this pull secret is generated by the hypershift operator
	pullSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-pull-secret", testHD.GetName()),
			Namespace: "default",
		},
		Data: map[string][]byte{
			".dockerconfigjson": []byte(`docker-pull-secret`),
		},
	}

	client.Create(ctx, pullSecret)

	hdr := &HypershiftDeploymentReconciler{
		Client: client,
		Log:    ctrl.Log.WithName("tester"),
	}

	_, err := hdr.Reconcile(ctx, ctrl.Request{NamespacedName: getNN})
	assert.Nil(t, err, "err nil when reconcile was successfull")

	manifestWorkKey := types.NamespacedName{
		Name:      generateManifestName(testHD),
		Namespace: helper.GetTargetManagedCluster(testHD)}

	requiredResource := map[kindAndKey]bool{
		kindAndKey{
			GroupVersionKind: schema.GroupVersionKind{
				Group: "hypershift.openshift.io", Version: "v1alpha1", Kind: "HostedCluster"},
			NamespacedName: genKeyFromObject(testHD)}: true,

		kindAndKey{
			GroupVersionKind: schema.GroupVersionKind{
				Group: "hypershift.openshift.io", Version: "v1alpha1", Kind: "NodePool"},
			NamespacedName: genKeyFromObject(testHD)}: true,

		kindAndKey{
			GroupVersionKind: schema.GroupVersionKind{
				Group: "", Version: "v1", Kind: "Secret"},
			NamespacedName: types.NamespacedName{
				Name: "test1-node-mgmt-creds", Namespace: helper.GetTargetNamespace(testHD)}}: true,

		kindAndKey{
			GroupVersionKind: schema.GroupVersionKind{
				Group: "", Version: "v1", Kind: "Secret"},
			NamespacedName: types.NamespacedName{
				Name: "test1-cpo-creds", Namespace: helper.GetTargetNamespace(testHD)}}: true,

		kindAndKey{
			GroupVersionKind: schema.GroupVersionKind{
				Group: "", Version: "v1", Kind: "Secret"},
			NamespacedName: types.NamespacedName{
				Name: "test1-node-mgmt-creds", Namespace: helper.GetTargetNamespace(testHD)}}: true,

		kindAndKey{
			GroupVersionKind: schema.GroupVersionKind{
				Group: "", Version: "v1", Kind: "Secret"},
			NamespacedName: types.NamespacedName{
				Name: "test1-pull-secret", Namespace: helper.GetTargetNamespace(testHD)}}: true,
	}

	checker, err := newManifestResourceChecker(ctx, client, manifestWorkKey)
	assert.Nil(t, err, "err nil when the mainfestwork check created")
	assert.Nil(t, checker.shouldHave(requiredResource), "err nil when all requrie resource exist in manifestwork")

}

// TestManifestWorkFlowWithExtraConfigurations test
// when override is set to manifestwork, test if the manifestwork is created
// and extra secret/configmap is put into manifestwork payload in addition to
// the required resource of TestManifestWorkFlow
func TestManifestWorkFlowWithExtraConfigurations(t *testing.T) {
	client := initClient()
	ctx := context.Background()

	infraOut := getAWSInfrastructureOut()
	testHD := getHypershiftDeployment("default", "test1")
	testHD.Spec.Override = hyd.InfraConfigureWithManifest

	testHD.Spec.Infrastructure.Platform = &hyd.Platforms{AWS: &hyd.AWSPlatform{}}
	testHD.Spec.Credentials = &hyd.CredentialARNs{AWS: &hyd.AWSCredentials{}}
	testHD.Spec.InfraID = infraOut.InfraID
	ScaffoldAWSHostedClusterSpec(testHD, infraOut)
	ScaffoldAWSNodePoolSpec(testHD, infraOut)

	cfgSecretName := "hostedcluster-config-secret-1"
	cfgConfigName := "hostedcluster-config-configmap-1"
	cfgItemSecretName := "hostedcluster-config-item-1"

	insertConfigSecretAndConfigMap := func() {
		testHD.Spec.HostedClusterSpec.Configuration = &hyp.ClusterConfiguration{}
		testHD.Spec.HostedClusterSpec.Configuration.SecretRefs = []corev1.LocalObjectReference{
			corev1.LocalObjectReference{Name: cfgSecretName}}

		testHD.Spec.HostedClusterSpec.Configuration.ConfigMapRefs = []corev1.LocalObjectReference{
			corev1.LocalObjectReference{Name: cfgConfigName}}

		testHD.Spec.HostedClusterSpec.Configuration.Items = []runtime.RawExtension{runtime.RawExtension{Object: &corev1.Secret{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Secret",
				APIVersion: corev1.SchemeGroupVersion.String(),
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      cfgItemSecretName,
				Namespace: testHD.GetNamespace(),
			},
			Data: map[string][]byte{
				".dockerconfigjson": []byte(`docker-pull-secret`),
			},
		},
		}}
	}

	insertConfigSecretAndConfigMap()

	// ensure the pull secret exist in cluster
	// this pull secret is generated by the hypershift operator
	pullSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-pull-secret", testHD.GetName()),
			Namespace: testHD.GetNamespace(),
		},
		Data: map[string][]byte{
			".dockerconfigjson": []byte(`docker-pull-secret`),
		},
	}

	client.Create(ctx, pullSecret)
	defer client.Delete(ctx, pullSecret)

	se := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfgSecretName,
			Namespace: testHD.GetNamespace(),
		},
		Data: map[string][]byte{
			".dockerconfigjson": []byte(`docker-pull-secret`),
		},
	}

	client.Create(ctx, se)
	defer client.Delete(ctx, se)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfgConfigName,
			Namespace: testHD.GetNamespace(),
		},
		Data: map[string]string{
			".dockerconfigjson": "docker-configmap",
		},
	}

	client.Create(ctx, cm)
	defer client.Delete(ctx, cm)

	client.Create(ctx, testHD)
	defer client.Delete(ctx, testHD)

	hdr := &HypershiftDeploymentReconciler{
		Client: client,
		Log:    ctrl.Log.WithName("tester"),
	}

	_, err := hdr.Reconcile(ctx, ctrl.Request{NamespacedName: getNN})
	assert.Nil(t, err, "err nil when reconcile was successfull")

	manifestWorkKey := types.NamespacedName{
		Name:      generateManifestName(testHD),
		Namespace: helper.GetTargetManagedCluster(testHD)}

	requiredResource := map[kindAndKey]bool{
		kindAndKey{
			GroupVersionKind: schema.GroupVersionKind{
				Group: "", Version: "v1", Kind: "Secret"},
			NamespacedName: types.NamespacedName{
				Name: cfgSecretName, Namespace: helper.GetTargetNamespace(testHD)}}: true,

		kindAndKey{
			GroupVersionKind: schema.GroupVersionKind{
				Group: "", Version: "v1", Kind: "Secret"},
			NamespacedName: types.NamespacedName{
				Name: cfgItemSecretName, Namespace: helper.GetTargetNamespace(testHD)}}: true,

		kindAndKey{
			GroupVersionKind: schema.GroupVersionKind{
				Group: "", Version: "v1", Kind: "ConfigMap"},
			NamespacedName: types.NamespacedName{
				Name: cfgConfigName, Namespace: helper.GetTargetNamespace(testHD)}}: true,
	}

	checker, err := newManifestResourceChecker(ctx, client, manifestWorkKey)
	assert.Nil(t, err, "err nil when the mainfestwork check created")
	assert.Nil(t, checker.shouldHave(requiredResource), "err nil when all requrie resource exist in manifestwork")
}

func manifesworkShouldHave(ctx context.Context, clt client.Client, manifestKey types.NamespacedName, required map[kindAndKey]bool) error {
	manifestWork := &workv1.ManifestWork{}

	if err := clt.Get(ctx, manifestKey, manifestWork); err != nil {
		return err
	}

	wl := manifestWork.Spec.Workload.Manifests

	got := map[kindAndKey]bool{}

	for _, w := range wl {
		u := &unstructured.Unstructured{}
		if err := json.Unmarshal(w.Raw, u); err != nil {
			return fmt.Errorf("faield convert manifest to unstructured, err: %w", err)
		}

		k := kindAndKey{
			GroupVersionKind: u.GetObjectKind().GroupVersionKind(),
			NamespacedName:   genKeyFromObject(u),
		}

		got[k] = true
	}

	for k, shouldExist := range required {
		if shouldExist {
			if !got[k] {
				return fmt.Errorf("%v should exist in manifestwork", k)
			}

			continue
		}

		if got[k] {
			return fmt.Errorf("%v shouldn't exist in manifestwork", k)
		}
	}

	return nil
}

type manifestworkChecker struct {
	clt      client.Client
	ctx      context.Context
	resource map[kindAndKey]bool
}

func newManifestResourceChecker(ctx context.Context, clt client.Client, key types.NamespacedName) (*manifestworkChecker, error) {
	manifestWork := &workv1.ManifestWork{}

	if err := clt.Get(ctx, key, manifestWork); err != nil {
		return nil, err
	}

	wl := manifestWork.Spec.Workload.Manifests

	got := map[kindAndKey]bool{}

	for _, w := range wl {
		u := &unstructured.Unstructured{}
		if err := json.Unmarshal(w.Raw, u); err != nil {
			return nil, fmt.Errorf("faield convert manifest to unstructured, err: %w", err)
		}

		k := kindAndKey{
			GroupVersionKind: u.GetObjectKind().GroupVersionKind(),
			NamespacedName:   genKeyFromObject(u),
		}

		got[k] = true
	}

	return &manifestworkChecker{
		clt:      clt,
		ctx:      ctx,
		resource: got,
	}, nil
}

func (m *manifestworkChecker) shouldHave(res map[kindAndKey]bool) error {
	for k, shouldExist := range res {
		if shouldExist {
			if !m.resource[k] {
				return fmt.Errorf("%v should exist in manifestwork", k)
			}

			continue
		}

		if m.resource[k] {
			return fmt.Errorf("%v shouldn't exist in manifestwork", k)
		}
	}

	return nil
}

func (m *manifestworkChecker) shouldNotHave(res map[kindAndKey]bool) error {
	for k, _ := range res {
		if _, ok := m.resource[k]; ok {
			return fmt.Errorf("%v shouldn't exist in manifestwork", k)
		}
	}

	return nil
}

func TestConfigureFalseWithManifestWork(t *testing.T) {

	client := initClient()

	testHD := getHypershiftDeployment(getNN.Namespace, getNN.Name)
	testHD.Spec.Override = hyd.InfraConfigureWithManifest
	testHD.Spec.TargetManagedCluster = "local-host"
	testHD.Spec.TargetNamespace = "multicluster-engine"

	client.Create(context.Background(), testHD)

	hdr := &HypershiftDeploymentReconciler{
		Client: client,
	}
	_, err := hdr.Reconcile(context.Background(), ctrl.Request{NamespacedName: getNN})
	assert.Nil(t, err, "err nil when reconcile was successful")

	var resultHD hyd.HypershiftDeployment
	err = client.Get(context.Background(), getNN, &resultHD)
	assert.Nil(t, err, "is nil when HypershiftDeployment resource is found")
	assert.True(t, meta.IsStatusConditionFalse(resultHD.Status.Conditions, string(hyd.WorkConfigured)), "is true when ManifestWork is not configured correctly")

	err = client.Delete(context.Background(), &resultHD)
	assert.Nil(t, err, "is nill when HypershiftDeployment resource is deleted")

	_, err = hdr.Reconcile(context.Background(), ctrl.Request{NamespacedName: getNN})
	assert.Nil(t, err, "err nil when reconcile on delete was successful")

	err = client.Get(context.Background(), getNN, &resultHD)
	assert.True(t, errors.IsNotFound(err), "is not found when HypershiftDeployment resource is deleted successfully")
}

// TestManifestWorkFlow tests when override is set to manifestwork, test if the manifestwork is created
// and referenece secret is put into manifestwork payload
func TestManifestWorkFlowWithSSHKey(t *testing.T) {
	client := initClient()

	ctx := context.Background()

	infraOut := getAWSInfrastructureOut()
	testHD := getHypershiftDeployment(getNN.Namespace, getNN.Name)
	testHD.Spec.Override = hyd.InfraConfigureWithManifest
	testHD.Spec.TargetManagedCluster = "local-host"
	testHD.Spec.TargetNamespace = "multicluster-engine"

	testHD.Spec.Infrastructure.Platform = &hyd.Platforms{AWS: &hyd.AWSPlatform{}}
	testHD.Spec.Credentials = &hyd.CredentialARNs{AWS: &hyd.AWSCredentials{}}
	testHD.Spec.InfraID = infraOut.InfraID
	ScaffoldAWSHostedClusterSpec(testHD, infraOut)
	ScaffoldAWSNodePoolSpec(testHD, infraOut)

	sshKeySecretName := fmt.Sprintf("%s-ssh-key", testHD.GetName())
	pullSecretName := fmt.Sprintf("%s-pull-secret", testHD.GetName())

	testHD.Spec.HostedClusterSpec.SSHKey.Name = sshKeySecretName

	client.Create(context.Background(), testHD)

	hdr := &HypershiftDeploymentReconciler{
		Client: client,
	}

	_, err := hdr.Reconcile(context.Background(), ctrl.Request{NamespacedName: getNN})
	assert.NotNil(t, err, "fail on missing pull secret")

	// ensure the pull secret exist in cluster
	// this pull secret is generated by the hypershift operator
	pullSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pullSecretName,
			Namespace: testHD.GetNamespace(),
		},
		Data: map[string][]byte{
			".dockerconfigjson": []byte(`docker-pull-secret`),
		},
	}

	client.Create(ctx, pullSecret)
	defer client.Delete(ctx, pullSecret)

	sshKeySecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sshKeySecretName,
			Namespace: testHD.GetNamespace(),
		},
		Data: map[string][]byte{
			".dockerconfigjson": []byte(`ssh-key-secret`),
		},
	}

	err = client.Create(ctx, sshKeySecret)
	assert.Nil(t, err, "err nil when creating ssh key secret")
	defer client.Delete(ctx, sshKeySecret)

	_, err = hdr.Reconcile(context.Background(), ctrl.Request{NamespacedName: getNN})
	assert.Nil(t, err, "err nil when reconcile was successful")

	manifestWorkKey := types.NamespacedName{
		Name:      generateManifestName(testHD),
		Namespace: helper.GetTargetManagedCluster(testHD)}

	requiredResource := map[kindAndKey]bool{
		kindAndKey{
			GroupVersionKind: schema.GroupVersionKind{
				Group: "", Version: "v1", Kind: "Secret"},
			NamespacedName: types.NamespacedName{
				Name: pullSecretName, Namespace: helper.GetTargetNamespace(testHD)}}: true,

		kindAndKey{
			GroupVersionKind: schema.GroupVersionKind{
				Group: "", Version: "v1", Kind: "Secret"},
			NamespacedName: types.NamespacedName{
				Name: sshKeySecretName, Namespace: helper.GetTargetNamespace(testHD)}}: true,
	}

	checker, err := newManifestResourceChecker(ctx, client, manifestWorkKey)
	assert.Nil(t, err, "err nil when the mainfestwork check created")
	assert.Nil(t, checker.shouldHave(requiredResource), "err nil when all requrie resource exist in manifestwork")

	t.Log("test hypershiftDeployment remove ssh key reference")
	update := func() bool {
		_, err := controllerutil.CreateOrUpdate(ctx, client, testHD, func() error {
			testHD.Spec.HostedClusterSpec.SSHKey.Name = ""
			return nil
		})

		if err != nil {
			t.Log(err)
			return false
		}

		return true
	}

	assert.Eventually(t, update, 20*time.Second, 5*time.Second)

	_, err = hdr.Reconcile(context.Background(), ctrl.Request{NamespacedName: getNN})
	assert.Nil(t, err, "err nil when reconcile was successful")

	deleted := map[kindAndKey]bool{
		kindAndKey{
			GroupVersionKind: schema.GroupVersionKind{
				Group: "", Version: "v1", Kind: "Secret"},
			NamespacedName: types.NamespacedName{
				Name: sshKeySecretName, Namespace: helper.GetTargetNamespace(testHD)}}: true,
	}

	checker, err = newManifestResourceChecker(ctx, client, manifestWorkKey)
	assert.Nil(t, err, "err nil when the mainfestwork check created")
	assert.Nil(t, checker.shouldNotHave(deleted), "err nil when all requrie resource exist in manifestwork")
}
