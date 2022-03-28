package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"testing"

	hyp "github.com/openshift/hypershift/api/v1alpha1"

	hyd "github.com/stolostron/hypershift-deployment-controller/api/v1alpha1"
	hypdeployment "github.com/stolostron/hypershift-deployment-controller/api/v1alpha1"
	"github.com/stolostron/hypershift-deployment-controller/pkg/helper"
	"github.com/stretchr/testify/assert"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/meta"
	condmeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
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

func getHDforManifestWork() *hyd.HypershiftDeployment {
	infraOut := getAWSInfrastructureOut()
	testHD := getHypershiftDeployment("default", "test1")
	testHD.Spec.Override = hyd.InfraConfigureWithManifest

	testHD.Spec.Infrastructure.Platform = &hyd.Platforms{AWS: &hyd.AWSPlatform{}}
	testHD.Spec.Credentials = &hyd.CredentialARNs{AWS: &hyd.AWSCredentials{}}
	testHD.Spec.InfraID = infraOut.InfraID
	ScaffoldAWSHostedClusterSpec(testHD, infraOut)
	ScaffoldAWSNodePoolSpec(testHD, infraOut)
	return testHD
}

type manifestworkChecker struct {
	clt      client.Client
	ctx      context.Context
	key      types.NamespacedName
	resource map[kindAndKey]bool
	obj      *workv1.ManifestWork
	status   workv1.ManifestWorkStatus
	spec     workv1.ManifestWorkSpec
}

func newManifestResourceChecker(ctx context.Context, clt client.Client, key types.NamespacedName) (*manifestworkChecker, error) {
	m := &manifestworkChecker{ctx: ctx, clt: clt, key: key}
	err := m.update()
	return m, err
}

func (m *manifestworkChecker) update() error {
	manifestWork := &workv1.ManifestWork{}
	if err := m.clt.Get(m.ctx, m.key, manifestWork); err != nil {
		return err
	}

	wl := manifestWork.Spec.Workload.Manifests

	got := map[kindAndKey]bool{}

	for _, w := range wl {
		u := &unstructured.Unstructured{}
		if err := json.Unmarshal(w.Raw, u); err != nil {
			return fmt.Errorf("failed convert manifest to unstructured, err: %w", err)
		}

		k := kindAndKey{
			GroupVersionKind: u.GetObjectKind().GroupVersionKind(),
			NamespacedName:   genKeyFromObject(u),
		}

		got[k] = true
	}

	m.resource = got
	m.status = manifestWork.Status
	m.spec = manifestWork.Spec
	m.obj = manifestWork

	return nil
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

// TestManifestWorkFlow tests when override is set to manifestwork, test if the manifestwork is created
// and reference secret is put into manifestwork payload
func TestManifestWorkFlowBaseCase(t *testing.T) {
	client := initClient()
	ctx := context.Background()

	testHD := getHDforManifestWork()
	testHD.Spec.TargetManagedCluster = "local-cluster"

	client.Create(ctx, testHD)
	defer client.Delete(ctx, testHD)

	// ensure the pull secret exist in cluster
	// this pull secret is generated by the hypershift operator
	client.Create(ctx, getPullSecret(testHD))

	hdr := &HypershiftDeploymentReconciler{
		Client: client,
		Log:    ctrl.Log.WithName("tester"),
	}

	_, err := hdr.Reconcile(ctx, ctrl.Request{NamespacedName: getNN})
	assert.Nil(t, err, "err nil when reconcile was successfull")

	requiredResource := map[kindAndKey]bool{
		{
			GroupVersionKind: schema.GroupVersionKind{
				Group: "", Version: "v1", Kind: "Namespace"},
			NamespacedName: types.NamespacedName{
				Name: "default", Namespace: ""}}: true,

		{
			GroupVersionKind: schema.GroupVersionKind{
				Group: "hypershift.openshift.io", Version: "v1alpha1", Kind: "HostedCluster"},
			NamespacedName: genKeyFromObject(testHD)}: true,

		{
			GroupVersionKind: schema.GroupVersionKind{
				Group: "hypershift.openshift.io", Version: "v1alpha1", Kind: "NodePool"},
			NamespacedName: genKeyFromObject(testHD)}: true,

		{
			GroupVersionKind: schema.GroupVersionKind{
				Group: "", Version: "v1", Kind: "Secret"},
			NamespacedName: types.NamespacedName{
				Name: "test1-node-mgmt-creds", Namespace: helper.GetTargetNamespace(testHD)}}: true,

		{
			GroupVersionKind: schema.GroupVersionKind{
				Group: "", Version: "v1", Kind: "Secret"},
			NamespacedName: types.NamespacedName{
				Name: "test1-cpo-creds", Namespace: helper.GetTargetNamespace(testHD)}}: true,

		{
			GroupVersionKind: schema.GroupVersionKind{
				Group: "", Version: "v1", Kind: "Secret"},
			NamespacedName: types.NamespacedName{
				Name: "test1-node-mgmt-creds", Namespace: helper.GetTargetNamespace(testHD)}}: true,

		{
			GroupVersionKind: schema.GroupVersionKind{
				Group: "", Version: "v1", Kind: "Secret"},
			NamespacedName: types.NamespacedName{
				Name: "test1-pull-secret", Namespace: helper.GetTargetNamespace(testHD)}}: true,
	}

	checker, err := newManifestResourceChecker(ctx, client, getManifestWorkKey(testHD))
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

	testHD := getHDforManifestWork()
	testHD.Spec.TargetManagedCluster = "local-cluster"

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
	pullSecret := getPullSecret(testHD)

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

	checker, err := newManifestResourceChecker(ctx, client, getManifestWorkKey(testHD))
	assert.Nil(t, err, "err nil when the mainfestwork check created")
	assert.Nil(t, checker.shouldHave(requiredResource), "err nil when all requrie resource exist in manifestwork")

}

func TestManifestWorkFlowNoTargetManagedCluster(t *testing.T) {
	client := initClient()
	ctx := context.Background()

	testHD := getHDforManifestWork()

	client.Create(ctx, testHD)
	defer client.Delete(ctx, testHD)

	// ensure the pull secret exist in cluster
	// this pull secret is generated by the hypershift operator
	client.Create(ctx, getPullSecret(testHD))

	hdr := &HypershiftDeploymentReconciler{
		Client: client,
		Log:    ctrl.Log.WithName("tester"),
	}

	_, err := hdr.Reconcile(ctx, ctrl.Request{NamespacedName: getNN})
	assert.Nil(t, err, "err nil when reconcile was successfull")

	var resultHD hyd.HypershiftDeployment
	err = client.Get(context.Background(), getNN, &resultHD)
	assert.Nil(t, err, "is nil when HypershiftDeployment resource is found")

	c := meta.FindStatusCondition(resultHD.Status.Conditions, string(hyd.WorkConfigured))
	t.Log("Condition msg: " + c.Message)
	assert.Equal(t, "Missing targetManagedCluster with override: MANIFESTWORK", c.Message, "is equal when targetManagedCluster is missing")
}

func TestManifestWorkFlowSpecCredentialsNil(t *testing.T) {
	client := initClient()
	ctx := context.Background()

	testHD := getHDforManifestWork()
	testHD.Spec.TargetManagedCluster = "local-cluster"
	testHD.Spec.Credentials = nil

	client.Create(ctx, testHD)
	defer client.Delete(ctx, testHD)

	// ensure the pull secret exist in cluster
	// this pull secret is generated by the hypershift operator
	client.Create(ctx, getPullSecret(testHD))

	hdr := &HypershiftDeploymentReconciler{
		Client: client,
		Log:    ctrl.Log.WithName("tester"),
	}

	_, err := hdr.Reconcile(ctx, ctrl.Request{NamespacedName: getNN})
	assert.Nil(t, err, "err nil when reconcile was successfull")

	var resultHD hyd.HypershiftDeployment
	err = client.Get(context.Background(), getNN, &resultHD)
	assert.Nil(t, err, "is nil when HypershiftDeployment resource is found")

	c := meta.FindStatusCondition(resultHD.Status.Conditions, string(hyd.PlatformIAMConfigured))
	t.Log("Condition msg: " + c.Message)
	assert.Equal(t, "Missing Spec.Crednetials.AWS.* platform IAM", c.Message, "is equal when spec.credentials is missing")
}

// TestManifestWorkFlow tests when override is set to manifestwork, test if the manifestwork is created
// and referenece secret is put into manifestwork payload
func TestManifestWorkFlowWithSSHKey(t *testing.T) {
	client := initClient()

	ctx := context.Background()

	testHD := getHDforManifestWork()
	testHD.Spec.TargetManagedCluster = "local-host"
	testHD.Spec.TargetNamespace = "multicluster-engine"

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

	checker, err := newManifestResourceChecker(ctx, client, getManifestWorkKey(testHD))
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

	checker, err = newManifestResourceChecker(ctx, client, getManifestWorkKey(testHD))
	assert.Nil(t, err, "err nil when the mainfestwork check created")
	assert.Nil(t, checker.shouldNotHave(deleted), "err nil when all requrie resource exist in manifestwork")
}

func TestManifestWorkSecrets(t *testing.T) {

	client := initClient()
	ctx := context.Background()

	testHD := getHDforManifestWork()
	testHD.Spec.TargetManagedCluster = "local-cluster"

	client.Create(ctx, testHD)
	defer client.Delete(ctx, testHD)

	// ensure the pull secret exist in cluster
	// this pull secret is generated by the hypershift operator
	client.Create(ctx, getPullSecret(testHD))

	hdr := &HypershiftDeploymentReconciler{
		Client: client,
		Log:    ctrl.Log.WithName("tester"),
	}

	_, err := hdr.Reconcile(ctx, ctrl.Request{NamespacedName: getNN})
	assert.Nil(t, err, "err nil when reconcile was successfull")

	manifestWorkKey := types.NamespacedName{
		Name:      generateManifestName(testHD),
		Namespace: helper.GetTargetManagedCluster(testHD)}

	var mw workv1.ManifestWork
	err = client.Get(ctx, manifestWorkKey, &mw)
	assert.Nil(t, err, "err nil when ManifestWork found")

	codecs := serializer.NewCodecFactory(client.Scheme())
	deserializer := codecs.UniversalDeserializer()

	p := testHD.Name
	secretNames := []string{p + "-pull-secret", p + "-cpo-creds", p + "-cloud-ctrl-creds", p + "-node-mgmt-creds"}
	for _, sc := range secretNames {
		found := false
		for _, manifest := range mw.Spec.Workload.Manifests {
			var s corev1.Secret
			_, gvk, _ := deserializer.Decode(manifest.Raw, nil, &s)
			if gvk.Kind == "Secret" && s.Name == sc {
				t.Log("Correctly identified Kind: " + gvk.Kind + " Name: " + s.Name)
				found = true
				break
			}
		}
		if !found {
			t.Error("Did not find secret", sc)
		}
	}
}

func TestManifestWorkCustomSecretNames(t *testing.T) {

	client := initClient()
	ctx := context.Background()

	testHD := getHDforManifestWork()
	testHD.Spec.TargetManagedCluster = "local-cluster"

	//Customize the secret names
	testHD.Spec.HostedClusterSpec.PullSecret.Name = "my-secret-to-pull"
	testHD.Spec.HostedClusterSpec.Platform.AWS.ControlPlaneOperatorCreds.Name = "my-control"
	testHD.Spec.HostedClusterSpec.Platform.AWS.KubeCloudControllerCreds.Name = "kube-creds-for-here"
	testHD.Spec.HostedClusterSpec.Platform.AWS.NodePoolManagementCreds.Name = "node-cred-may-i-use"

	client.Create(ctx, testHD)
	defer client.Delete(ctx, testHD)

	// Use a custom name for the pull secret
	pullSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-secret-to-pull",
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

	var mw workv1.ManifestWork
	err = client.Get(ctx, manifestWorkKey, &mw)
	assert.Nil(t, err, "err nil when ManifestWork found")

	codecs := serializer.NewCodecFactory(client.Scheme())
	deserializer := codecs.UniversalDeserializer()

	secretNames := []string{"my-secret-to-pull", "my-control", "kube-creds-for-here", "node-cred-may-i-use"}
	for _, sc := range secretNames {
		found := false
		for _, manifest := range mw.Spec.Workload.Manifests {
			var s corev1.Secret
			_, gvk, _ := deserializer.Decode(manifest.Raw, nil, &s)
			if gvk.Kind == "Secret" && s.Name == sc {
				t.Log("Correctly identified Kind: " + gvk.Kind + " Name: " + s.Name)
				found = true
				break
			}
		}
		if !found {
			t.Error("Did not find secret", sc)
		}
	}
}

func TestManifestWorkStatusUpsertToHypershiftDeployment(t *testing.T) {
	clt := initClient()
	ctx := context.Background()

	hdr := &HypershiftDeploymentReconciler{
		Client: clt,
	}

	testHD := getHDforManifestWork()
	testHD.Spec.TargetManagedCluster = "local-host"
	testHD.Spec.TargetNamespace = "multicluster-engine"

	clt.Create(ctx, testHD)
	defer clt.Delete(ctx, testHD)

	// ensure the pull secret exist in cluster
	// this pull secret is generated by the hypershift operator
	pullSecret := getPullSecret(testHD)

	clt.Create(ctx, pullSecret)
	defer clt.Delete(ctx, pullSecret)

	_, err := hdr.Reconcile(context.Background(), ctrl.Request{NamespacedName: getNN})
	assert.Nil(t, err, "err nil when reconcile was successful")

	checker, err := newManifestResourceChecker(ctx, clt, getManifestWorkKey(testHD))
	assert.Nil(t, err, "err nil when the mainfestwork check created")

	assert.True(t, len(checker.spec.ManifestConfigs) != 0, "should have manifestconfigs")

	assert.Nil(t, checker.update(), "err nil when can get the target manifestwork")

	resStr := "test"
	trueStr := "True"
	falseStr := "False"
	msgStr := "nope"
	progress := "Partial"

	_ = falseStr

	resStr1 := "WaitingForAvailableMachines"

	manifestWork := checker.obj

	origin := manifestWork.DeepCopy()

	hcCondInput := workv1.ManifestCondition{
		ResourceMeta: workv1.ManifestResourceMeta{
			Group:     hyp.GroupVersion.Group,
			Resource:  HostedClusterResource,
			Name:      testHD.Name,
			Namespace: helper.GetTargetNamespace(testHD),
		},
		StatusFeedbacks: workv1.StatusFeedbackResult{
			Values: []workv1.FeedbackValue{
				{
					Name: Reason,
					Value: workv1.FieldValue{
						Type:   workv1.String,
						String: &resStr,
					},
				},
				{
					Name: StatusFlag,
					Value: workv1.FieldValue{
						Type:   workv1.String,
						String: &trueStr,
					},
				},
				{
					Name: Message,
					Value: workv1.FieldValue{
						Type:   workv1.String,
						String: &msgStr,
					},
				},
				{
					Name: Progress,
					Value: workv1.FieldValue{
						Type:   workv1.String,
						String: &progress,
					},
				},
			},
		},
	}

	nodepoolInput := []workv1.ManifestCondition{
		{
			ResourceMeta: workv1.ManifestResourceMeta{
				Group:     hyp.GroupVersion.Group,
				Resource:  NodePoolResource,
				Name:      testHD.Name,
				Namespace: helper.GetTargetNamespace(testHD),
			},
			StatusFeedbacks: workv1.StatusFeedbackResult{
				Values: []workv1.FeedbackValue{
					{
						Name: Reason,
						Value: workv1.FieldValue{
							Type:   workv1.String,
							String: &resStr,
						},
					},
					{
						Name: StatusFlag,
						Value: workv1.FieldValue{
							Type:   workv1.String,
							String: &trueStr,
						},
					},
					{
						Name: Message,
						Value: workv1.FieldValue{
							Type:   workv1.String,
							String: &msgStr,
						},
					},
				},
			},
		},

		{
			ResourceMeta: workv1.ManifestResourceMeta{
				Group:     hyp.GroupVersion.Group,
				Resource:  NodePoolResource,
				Name:      testHD.Name,
				Namespace: helper.GetTargetNamespace(testHD),
			},
			StatusFeedbacks: workv1.StatusFeedbackResult{
				Values: []workv1.FeedbackValue{
					{
						Name: Reason,
						Value: workv1.FieldValue{
							Type:   workv1.String,
							String: &resStr1,
						},
					},
					{
						Name: StatusFlag,
						Value: workv1.FieldValue{
							Type:   workv1.String,
							String: &falseStr,
						},
					},
					{
						Name: Message,
						Value: workv1.FieldValue{
							Type:   workv1.String,
							String: &msgStr,
						},
					},
				},
			},
		},
	}

	feedbackFine := workv1.ManifestResourceStatus{
		Manifests: append(nodepoolInput, hcCondInput),
	}

	manifestWork.Status.ResourceStatus = feedbackFine

	assert.Nil(t, clt.Status().Patch(ctx, manifestWork, client.MergeFrom(origin)), "err nil when update manifetwork status")

	checker.update()

	workNodepoolCond := checker.status.ResourceStatus.Manifests

	assert.Len(t, workNodepoolCond, 3, "should have 3 feedbacks")

	_, err = hdr.Reconcile(context.Background(), ctrl.Request{NamespacedName: getNN})
	assert.Nil(t, err, "err nil when reconcile was successful")

	updatedHD := &hyd.HypershiftDeployment{}
	assert.Nil(t, clt.Get(ctx, getNN, updatedHD), "err nil Get updated hypershiftDeployment")

	hcAvaCond := condmeta.FindStatusCondition(updatedHD.Status.Conditions, string(hypdeployment.HostedClusterAvaliable))

	assert.NotNil(t, hcAvaCond, "not nil, should find a hostedcluster condition")
	assert.NotEmpty(t, hcAvaCond.Reason, "condition reason should be nil")

	hcProCond := condmeta.FindStatusCondition(updatedHD.Status.Conditions, string(hypdeployment.HostedClusterProgress))

	assert.NotNil(t, hcProCond, "not nil, should find a hostedcluster condition")
	assert.NotEmpty(t, hcProCond.Reason, "condition reason should be nil")

	nodepoolCond := condmeta.FindStatusCondition(updatedHD.Status.Conditions, string(hypdeployment.Nodepool))

	assert.NotNil(t, nodepoolCond, "not nil, should find a hostedcluster condition")
	assert.NotEmpty(t, nodepoolCond.Reason, "condition reason should be nil")
	assert.True(t, nodepoolCond.Reason == resStr1, "true, only contain a failed reason")
}
