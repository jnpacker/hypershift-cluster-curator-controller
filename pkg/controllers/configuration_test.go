package controllers

import (
	"context"
	"encoding/json"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	apifixtures "github.com/openshift/hypershift/api/fixtures"
	hyp "github.com/openshift/hypershift/api/v1alpha1"
	hyd "github.com/stolostron/hypershift-deployment-controller/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	workv1 "open-cluster-management.io/api/work/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func getHDforSecretEncryption(config bool) *hyd.HypershiftDeployment {
	return &hyd.HypershiftDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
			Annotations: map[string]string{
				"test1": "doNotTransfer1",
				"test2": "doNotTransfer2",
			},
		},
		Spec: hyd.HypershiftDeploymentSpec{
			HostingCluster:   "local-cluster",
			HostingNamespace: "clusters",
			Infrastructure: hyd.InfraSpec{
				Configure: config,
				Platform: &hyd.Platforms{
					AWS: &hyd.AWSPlatform{Region: "us-east-1"},
				},
			},
			InfraID: "test1-abcde",
		},
	}
}

// TestHDEncryptionSecret tests if the manifestwork is created
// with the encryption secret
func TestHDEncryptionSecret(t *testing.T) {
	r := GetHypershiftDeploymentReconciler()
	ctx := context.Background()

	// Create AESCBC active key secret
	exampleOptions := &apifixtures.ExampleOptions{
		Name:      "test-my",
		Namespace: "default",
	}
	userActiveKeySecret := exampleOptions.EtcdEncryptionKeySecret()
	err := r.Create(ctx, userActiveKeySecret)
	defer r.Delete(ctx, userActiveKeySecret)
	assert.Nil(t, err, "active encryption secret should be created with no error")

	// Create AESCBC backup key secret
	userBackupKeySecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-my-backup-key",
			Namespace: "default",
		},
		Data: map[string][]byte{
			".dockerconfigjson": []byte(`docker-pull-secret`),
		},
	}
	err = r.Create(ctx, userBackupKeySecret)
	defer r.Delete(ctx, userBackupKeySecret)
	assert.Nil(t, err, "backup encryption secret should be created with no error")

	// Test configure=T - not encryption secret - generate AESCBC encryption secret
	configTHD := getHDforSecretEncryption(true)
	scaffoldHostedClusterSpec(configTHD)
	assert.Equal(t, hyp.AESCBC, configTHD.Spec.HostedClusterSpec.SecretEncryption.Type, "secretEncryption should default to AESCBC for configure=T")
	assert.Equal(t, configTHD.Name+"-etcd-encryption-key", configTHD.Spec.HostedClusterSpec.SecretEncryption.AESCBC.ActiveKey.Name, "AESCBC active key is not set correctly for secret encryption")

	m, err := ScaffoldManifestwork(configTHD)
	assert.Nil(t, err)

	payload := []workv1.Manifest{}
	loadManifest := r.ensureConfiguration(ctx, m)
	err = loadManifest(configTHD, &payload)
	assert.Nil(t, err)
	assert.Len(t, payload, 1, "only 1 manifestwork payload which is the generated encryption secret")
	payload0Obj := payload[0].Object
	assert.Equal(t, "Secret", payload0Obj.GetObjectKind().GroupVersionKind().Kind)
	assert.Equal(t, configTHD.Name+"-etcd-encryption-key", payload0Obj.(*corev1.Secret).Name)
	assert.Equal(t, configTHD.Spec.HostingNamespace, payload0Obj.(*corev1.Secret).Namespace)

	// Test configure=T - use encryption secret found in old manifestwork payload
	payload[0].Raw, _ = json.Marshal(payload0Obj)
	m.Spec.Workload.Manifests = payload
	payload2 := []workv1.Manifest{}
	loadManifest = r.ensureConfiguration(ctx, m)
	err = loadManifest(configTHD, &payload2)
	assert.Nil(t, err)
	assert.Len(t, payload2, 1, "only 1 manifestwork payload which is the generated encryption secret")
	payload0Obj = payload2[0].Object
	assert.Equal(t, "Secret", payload0Obj.GetObjectKind().GroupVersionKind().Kind)
	assert.Equal(t, configTHD.Name+"-etcd-encryption-key", payload0Obj.(*corev1.Secret).Name)
	assert.Equal(t, configTHD.Spec.HostingNamespace, payload0Obj.(*corev1.Secret).Namespace)
	assert.Equal(t, m.Spec.Workload.Manifests[0].Object.(*corev1.Secret).Data, payload0Obj.(*corev1.Secret).Data, "encrypt secet in payload should match the secret is the manifestwork")

	// Test configure=T - user provided encryption secret
	configTHD.Spec.HostedClusterSpec.SecretEncryption = &hyp.SecretEncryptionSpec{
		Type: hyp.AESCBC,
		AESCBC: &hyp.AESCBCSpec{
			ActiveKey: corev1.LocalObjectReference{
				Name: "test-my-etcd-encryption-key",
			},
		},
	}
	m, _ = ScaffoldManifestwork(configTHD)
	payload3 := []workv1.Manifest{}
	loadManifest = r.ensureConfiguration(ctx, m)
	err = loadManifest(configTHD, &payload3)
	assert.Nil(t, err)
	assert.Len(t, payload3, 1, "only 1 manifestwork payload which is the generated encryption secret")
	payload0Obj = payload3[0].Object
	assert.Equal(t, "Secret", payload0Obj.GetObjectKind().GroupVersionKind().Kind)
	assert.Equal(t, "test-my-etcd-encryption-key", payload0Obj.(*corev1.Secret).Name)
	assert.Equal(t, configTHD.Spec.HostingNamespace, payload0Obj.(*corev1.Secret).Namespace)

	// Test configure=T - user provided activekey encryption secret not found - generate it
	configTHD.Spec.HostedClusterSpec.SecretEncryption = &hyp.SecretEncryptionSpec{
		Type: hyp.AESCBC,
		AESCBC: &hyp.AESCBCSpec{
			ActiveKey: corev1.LocalObjectReference{
				Name: "encryption-key-not-found",
			},
		},
	}
	m, _ = ScaffoldManifestwork(configTHD)
	payload4 := []workv1.Manifest{}
	loadManifest = r.ensureConfiguration(ctx, m)
	err = loadManifest(configTHD, &payload4)
	assert.Nil(t, err)
	assert.Len(t, payload4, 1, "only 1 manifestwork payload which is the generated encryption secret")
	payload0Obj = payload4[0].Object
	assert.Equal(t, "Secret", payload0Obj.GetObjectKind().GroupVersionKind().Kind)
	assert.Equal(t, "encryption-key-not-found", payload0Obj.(*corev1.Secret).Name)
	assert.Equal(t, configTHD.Spec.HostingNamespace, payload0Obj.(*corev1.Secret).Namespace)

	// Test configure=T - user provided backup encryption secret not found - error
	configTHD.Spec.HostedClusterSpec.SecretEncryption = &hyp.SecretEncryptionSpec{
		Type: hyp.AESCBC,
		AESCBC: &hyp.AESCBCSpec{
			ActiveKey: corev1.LocalObjectReference{
				Name: "encryption-key-not-found",
			},
			BackupKey: &corev1.LocalObjectReference{
				Name: "encryption-key-not-found",
			},
		},
	}
	m, _ = ScaffoldManifestwork(configTHD)
	payload4 = []workv1.Manifest{}
	loadManifest = r.ensureConfiguration(ctx, m)
	err = loadManifest(configTHD, &payload4)
	assert.Len(t, err.(utilerrors.Aggregate).Errors(), 1, "backupkey encryption secret not found")

	// Test configure=F - no secret encryption
	configFHD := getHDforSecretEncryption(false)
	scaffoldHostedClusterSpec(configFHD)
	assert.Nil(t, configFHD.Spec.HostedClusterSpec.SecretEncryption, "secretEncryption should be nil for configure=F")
	m, err = ScaffoldManifestwork(configFHD)
	assert.Nil(t, err)

	payload5 := []workv1.Manifest{}
	loadManifest = r.ensureConfiguration(ctx, m)
	err = loadManifest(configFHD, &payload5)
	assert.Nil(t, err)
	assert.Len(t, payload5, 0, "no manifestwork payload should be created")

	// Test configure=F - use provided encryption secret
	configFHD.Spec.HostedClusterSpec.SecretEncryption = &hyp.SecretEncryptionSpec{
		Type: hyp.AESCBC,
		AESCBC: &hyp.AESCBCSpec{
			ActiveKey: corev1.LocalObjectReference{
				Name: "test-my-etcd-encryption-key",
			},
			BackupKey: &corev1.LocalObjectReference{
				Name: "test-my-backup-key",
			},
		},
	}
	m, _ = ScaffoldManifestwork(configFHD)
	payload6 := []workv1.Manifest{}
	loadManifest = r.ensureConfiguration(ctx, m)
	err = loadManifest(configFHD, &payload6)
	assert.Nil(t, err)
	assert.Len(t, payload6, 2, "2 manifestwork payload which is the active & backup encryption secret")
	payload0Obj = payload6[0].Object
	assert.Equal(t, "Secret", payload0Obj.GetObjectKind().GroupVersionKind().Kind)
	assert.Equal(t, userActiveKeySecret.Name, payload0Obj.(*corev1.Secret).Name)
	assert.Equal(t, configFHD.Spec.HostingNamespace, payload0Obj.(*corev1.Secret).Namespace)
	payload1Obj := payload6[1].Object
	assert.Equal(t, "Secret", payload1Obj.GetObjectKind().GroupVersionKind().Kind)
	assert.Equal(t, userBackupKeySecret.Name, payload1Obj.(*corev1.Secret).Name)
	assert.Equal(t, configFHD.Spec.HostingNamespace, payload1Obj.(*corev1.Secret).Namespace)

	// Test configure=F - use encryption secret found instead of old manifestwork payload
	payload0Obj.(*corev1.Secret).Data["test"] = []byte(`aes_activekey`)
	payload6[0].Raw, _ = json.Marshal(payload0Obj)
	payload1Obj.(*corev1.Secret).Data["test"] = []byte(`aes_backupkey`)
	payload6[1].Raw, _ = json.Marshal(payload1Obj)
	m.Spec.Workload.Manifests = payload6
	payload7 := []workv1.Manifest{}
	loadManifest = r.ensureConfiguration(ctx, m)
	err = loadManifest(configFHD, &payload7)
	assert.Nil(t, err)
	assert.Len(t, payload6, 2, "2 manifestwork payload which is the active & backup encryption secret")
	assert.Equal(t, userActiveKeySecret.Data, payload7[0].Object.(*corev1.Secret).Data, "active encrypt secret in payload should match the user-specified secret")
	assert.Equal(t, userBackupKeySecret.Data, payload7[1].Object.(*corev1.Secret).Data, "backup encrypt secret in payload should match the user-specified secret")

	// Test configure=F - activekey encryption secret not found - use secret in manifestwork
	configFHD.Spec.HostedClusterSpec.SecretEncryption = &hyp.SecretEncryptionSpec{
		Type: hyp.AESCBC,
		AESCBC: &hyp.AESCBCSpec{
			ActiveKey: corev1.LocalObjectReference{
				Name: "encryption-key-not-found",
			},
			BackupKey: &corev1.LocalObjectReference{
				Name: "encryption-key-not-found",
			},
		},
	}
	payload0Obj.(*corev1.Secret).Name = "encryption-key-not-found"
	payload7[0].Raw, _ = json.Marshal(payload0Obj)
	payload1Obj.(*corev1.Secret).Name = "encryption-key-not-found"
	payload7[1].Raw, _ = json.Marshal(payload1Obj)
	m.Spec.Workload.Manifests = payload7
	payload8 := []workv1.Manifest{}
	loadManifest = r.ensureConfiguration(ctx, m)
	err = loadManifest(configFHD, &payload8)
	assert.Nil(t, err)
	assert.Len(t, payload8, 2, "2 manifestwork payload which is the active & backup encryption secret")
	assert.Equal(t, "encryption-key-not-found", payload8[0].Object.(*corev1.Secret).Name, "active encrypt secret in payload should match the user-specified secret")
	assert.Equal(t, "encryption-key-not-found", payload8[1].Object.(*corev1.Secret).Name, "backup encrypt secret in payload should match the user-specified secret")

	// Test configure=F - activekey encryption secret not found and not in manifestwork - fail
	configFHD.Spec.HostedClusterSpec.SecretEncryption = &hyp.SecretEncryptionSpec{
		Type: hyp.AESCBC,
		AESCBC: &hyp.AESCBCSpec{
			ActiveKey: corev1.LocalObjectReference{
				Name: "encryption-key-not-found",
			},
			BackupKey: &corev1.LocalObjectReference{
				Name: "encryption-key-not-found",
			},
		},
	}
	m, _ = ScaffoldManifestwork(configFHD)
	payload9 := []workv1.Manifest{}
	loadManifest = r.ensureConfiguration(ctx, m)
	err = loadManifest(configFHD, &payload9)
	assert.Len(t, err.(utilerrors.Aggregate).Errors(), 2, "2 encryption secrets (active and backup) not found")

}

func TestHDKmsEncryptionSecret(t *testing.T) {
	r := GetHypershiftDeploymentReconciler()
	ctx := context.Background()

	kmsSec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-kms-key",
			Namespace: "default",
		},
		Data: map[string][]byte{
			".dockerconfigjson": []byte(`docker-pull-secret`),
		},
	}
	err := r.Create(ctx, kmsSec)
	defer r.Delete(ctx, kmsSec)
	assert.Nil(t, err, "kms encryption secret should be created with no error")

	// Test configure=T - use provided KMS encryption secret
	configTHD := getHDforSecretEncryption(true)
	scaffoldHostedClusterSpec(configTHD)
	configTHD.Spec.HostedClusterSpec.SecretEncryption = &hyp.SecretEncryptionSpec{
		Type: hyp.KMS,
		KMS: &hyp.KMSSpec{
			AWS: &hyp.AWSKMSSpec{
				Auth: hyp.AWSKMSAuthSpec{
					Credentials: corev1.LocalObjectReference{
						Name: "test-kms-key",
					},
				},
			},
		},
	}

	m, err := ScaffoldManifestwork(configTHD)
	assert.Nil(t, err)
	payload := []workv1.Manifest{}
	loadManifest := r.ensureConfiguration(ctx, m)
	err = loadManifest(configTHD, &payload)
	assert.Nil(t, err)
	assert.Len(t, payload, 1, "only 1 manifestwork payload which is the kms encryption secret")
	payload0Obj := payload[0].Object
	assert.Equal(t, "Secret", payload0Obj.GetObjectKind().GroupVersionKind().Kind)
	assert.Equal(t, "test-kms-key", payload0Obj.(*corev1.Secret).Name)
	assert.Equal(t, configTHD.Spec.HostingNamespace, payload0Obj.(*corev1.Secret).Namespace)

	// Test configure=T - user-specified KMS encryption secret not found, use secret in old manifestwork payload
	configTHD.Spec.HostedClusterSpec.SecretEncryption = &hyp.SecretEncryptionSpec{
		Type: hyp.KMS,
		KMS: &hyp.KMSSpec{
			AWS: &hyp.AWSKMSSpec{
				Auth: hyp.AWSKMSAuthSpec{
					Credentials: corev1.LocalObjectReference{
						Name: "test-kms-key-not-found",
					},
				},
			},
		},
	}
	payload0Obj.(*corev1.Secret).Name = "test-kms-key-not-found"
	payload[0].Raw, _ = json.Marshal(payload0Obj)
	m.Spec.Workload.Manifests = payload
	payload2 := []workv1.Manifest{}
	loadManifest = r.ensureConfiguration(ctx, m)
	err = loadManifest(configTHD, &payload2)
	assert.Nil(t, err)
	assert.Len(t, payload2, 1, "only 1 manifestwork payload which is the generated encryption secret")
	payload0Obj = payload2[0].Object
	assert.Equal(t, "Secret", payload0Obj.GetObjectKind().GroupVersionKind().Kind)
	assert.Equal(t, "test-kms-key-not-found", payload0Obj.(*corev1.Secret).Name)

	// Test configure=T - KMS encryption secret not found
	configTHD.Spec.HostedClusterSpec.SecretEncryption = &hyp.SecretEncryptionSpec{
		Type: hyp.KMS,
		KMS: &hyp.KMSSpec{
			AWS: &hyp.AWSKMSSpec{
				Auth: hyp.AWSKMSAuthSpec{
					Credentials: corev1.LocalObjectReference{
						Name: "test-kms-key-not-found",
					},
				},
			},
		},
	}
	m, _ = ScaffoldManifestwork(configTHD)
	payload3 := []workv1.Manifest{}
	loadManifest = r.ensureConfiguration(ctx, m)
	err = loadManifest(configTHD, &payload3)
	assert.Len(t, err.(utilerrors.Aggregate).Errors(), 1, "kms encryption secrets not found")
}

func TestHCOnlyConfigItems(t *testing.T) {
	r := GetHypershiftDeploymentReconciler()
	ctx := context.Background()
	configFHD := getHDforSecretEncryption(false)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-secret",
			Namespace: "test",
		},
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: corev1.SchemeGroupVersion.String(),
		},
		Type: corev1.SecretTypeOpaque,
	}
	secretRaw, _ := json.Marshal(secret)
	secretItem := runtime.RawExtension{
		Raw: secretRaw,
	}
	configFHD.Spec.HostedClusterOnlyConfigItems = []runtime.RawExtension{secretItem}

	// HostedClusterOnlyConfigItems should be scaffolded to HD.Spec.HostedClusterSpec.Configuration.Items
	scaffoldHostedClusterSpec(configFHD)
	hcItems := configFHD.Spec.HostedClusterSpec.Configuration.Items
	assert.Len(t, hcItems, 1, "has secret in hostedcluster configuration items")
	hcSecret := &corev1.Secret{}
	json.Unmarshal(hcItems[0].Raw, hcSecret)
	assert.Equal(t, secret, hcSecret, "secret is hostedclusterSpec matches the secret in the HostedClusterOnlyConfigItems")

	// manifestwork payload should not contain the config item
	m, err := ScaffoldManifestwork(configFHD)
	assert.Nil(t, err)
	payload := []workv1.Manifest{}
	loadManifest := r.ensureConfiguration(ctx, m)
	err = loadManifest(configFHD, &payload)
	assert.Nil(t, err)
	assert.Len(t, payload, 0, "hc-only secret is not in the manifestwork payload")
}
