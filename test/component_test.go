package test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	helper "github.com/cloudposse/test-helpers/pkg/atmos/component-helper"
	awsHelper "github.com/cloudposse/test-helpers/pkg/aws"
	"github.com/cloudposse/test-helpers/pkg/atmos"
	"github.com/cloudposse/test-helpers/pkg/helm"
	"github.com/gruntwork-io/terratest/modules/random"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

type ComponentSuite struct {
	helper.TestSuite
}

func (s *ComponentSuite) TestBasic() {
	const component = "eks/vertical-pod-autoscaler/basic"
	const stack = "default-test"
	const awsRegion = "us-east-2"

	clusterOptions := s.GetAtmosOptions("eks/cluster", stack, nil)
	clusterId := atmos.Output(s.T(), clusterOptions, "eks_cluster_id")
	cluster := awsHelper.GetEksCluster(s.T(), context.Background(), awsRegion, clusterId)

	randomID := strings.ToLower(random.UniqueId())
	namespace := fmt.Sprintf("vpa-%s", randomID)

	inputs := map[string]interface{}{
		"kubernetes_namespace": namespace,
	}

	defer s.DestroyAtmosComponent(s.T(), component, stack, &inputs)
	options, _ := s.DeployAtmosComponent(s.T(), component, stack, &inputs)
	assert.NotNil(s.T(), options)

	metadata := helm.Metadata{}
	atmos.OutputStruct(s.T(), options, "metadata", &metadata)

	assert.Equal(s.T(), metadata.Chart, "vpa")
	assert.NotNil(s.T(), metadata.FirstDeployed)
	assert.NotNil(s.T(), metadata.LastDeployed)
	assert.NotEmpty(s.T(), metadata.Name)
	assert.Equal(s.T(), metadata.Namespace, namespace)
	assert.Equal(s.T(), metadata.Revision, 1)
	assert.NotNil(s.T(), metadata.Values)

	config, err := awsHelper.NewK8SClientConfig(cluster)
	assert.NoError(s.T(), err)
	assert.NotNil(s.T(), config)

	clientset, err := awsHelper.NewK8SClientset(cluster)
	assert.NoError(s.T(), err)
	assert.NotNil(s.T(), clientset)

	// Find VPA deployments dynamically
	deployments, err := clientset.AppsV1().Deployments(namespace).List(context.Background(), metav1.ListOptions{})
	assert.NoError(s.T(), err)
	assert.NotEmpty(s.T(), deployments.Items)

	// Verify VPA recommender deployment exists and is running
	recommenderDeployment := s.findDeploymentByNameSuffix(deployments.Items, "recommender")
	assert.NotNil(s.T(), recommenderDeployment, "VPA recommender deployment not found")
	assert.Equal(s.T(), *recommenderDeployment.Spec.Replicas, int32(1))

	// Wait for VPA recommender to be ready
	s.waitForDeploymentReady(clientset, namespace, recommenderDeployment.Name)

	// Test VPA CRD availability by creating a simple VPA resource
	dynamicClient, err := dynamic.NewForConfig(config)
	assert.NoError(s.T(), err)

	testAppName := fmt.Sprintf("test-app-%s", randomID)
	s.createTestApplication(clientset, namespace, testAppName)
	defer s.cleanupTestApplication(clientset, namespace, testAppName)

	// Create and verify VPA resource can be created (proves CRDs are installed)
	s.createVPAResource(dynamicClient, namespace, testAppName)
	defer s.cleanupVPAResource(dynamicClient, namespace, testAppName)

	// Verify VPA resource was created successfully
	s.verifyVPAResourceExists(dynamicClient, namespace, testAppName)

	s.DriftTest(component, stack, &inputs)
}

func (s *ComponentSuite) TestEnabledFlag() {
	const component = "eks/vertical-pod-autoscaler/disabled"
	const stack = "default-test"
	s.VerifyEnabledFlag(component, stack, nil)
}

func (s *ComponentSuite) SetupSuite() {
	s.TestSuite.InitConfig()
	s.TestSuite.Config.ComponentDestDir = "components/terraform/eks/vertical-pod-autoscaler"
	s.TestSuite.SetupSuite()
}

func TestRunSuite(t *testing.T) {
	suite := new(ComponentSuite)
	suite.AddDependency(t, "vpc", "default-test", nil)
	suite.AddDependency(t, "eks/cluster", "default-test", nil)
	helper.Run(t, suite)
}

// Helper functions

func (s *ComponentSuite) findDeploymentByNameSuffix(deployments []appsv1.Deployment, suffix string) *appsv1.Deployment {
	for i := range deployments {
		if strings.HasSuffix(deployments[i].Name, suffix) {
			return &deployments[i]
		}
	}
	return nil
}

func (s *ComponentSuite) waitForDeploymentReady(clientset *awsHelper.K8sClientSet, namespace, deploymentName string) {
	for i := 0; i < 30; i++ { // Wait up to 5 minutes
		deployment, err := clientset.AppsV1().Deployments(namespace).Get(context.Background(), deploymentName, metav1.GetOptions{})
		if err == nil && deployment.Status.ReadyReplicas == *deployment.Spec.Replicas {
			return
		}
		time.Sleep(10 * time.Second)
	}
	assert.Fail(s.T(), fmt.Sprintf("Deployment %s did not become ready within 5 minutes", deploymentName))
}

func (s *ComponentSuite) createTestApplication(clientset *awsHelper.K8sClientSet, namespace, appName string) {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      appName,
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &[]int32{1}[0],
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": appName,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": appName,
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "nginx:stable-alpine",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									"cpu":    "100m",
									"memory": "128Mi",
								},
							},
						},
					},
				},
			},
		},
	}

	_, err := clientset.AppsV1().Deployments(namespace).Create(context.Background(), deployment, metav1.CreateOptions{})
	assert.NoError(s.T(), err)
}

func (s *ComponentSuite) cleanupTestApplication(clientset *awsHelper.K8sClientSet, namespace, appName string) {
	err := clientset.AppsV1().Deployments(namespace).Delete(context.Background(), appName, metav1.DeleteOptions{})
	if err != nil {
		fmt.Printf("Error deleting test deployment %s: %v\n", appName, err)
	}
}

func (s *ComponentSuite) createVPAResource(dynamicClient dynamic.Interface, namespace, appName string) {
	vpa := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "autoscaling.k8s.io/v1",
			"kind":       "VerticalPodAutoscaler",
			"metadata": map[string]interface{}{
				"name":      fmt.Sprintf("%s-vpa", appName),
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"targetRef": map[string]interface{}{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"name":       appName,
				},
				"updatePolicy": map[string]interface{}{
					"updateMode": "Off", // Recommendation-only mode
				},
			},
		},
	}

	vpaGVR := schema.GroupVersionResource{
		Group:    "autoscaling.k8s.io",
		Version:  "v1",
		Resource: "verticalpodautoscalers",
	}

	_, err := dynamicClient.Resource(vpaGVR).Namespace(namespace).Create(context.Background(), vpa, metav1.CreateOptions{})
	assert.NoError(s.T(), err)
}

func (s *ComponentSuite) cleanupVPAResource(dynamicClient dynamic.Interface, namespace, appName string) {
	vpaGVR := schema.GroupVersionResource{
		Group:    "autoscaling.k8s.io",
		Version:  "v1",
		Resource: "verticalpodautoscalers",
	}

	err := dynamicClient.Resource(vpaGVR).Namespace(namespace).Delete(context.Background(), fmt.Sprintf("%s-vpa", appName), metav1.DeleteOptions{})
	if err != nil {
		fmt.Printf("Error deleting VPA resource %s-vpa: %v\n", appName, err)
	}
}

func (s *ComponentSuite) verifyVPAResourceExists(dynamicClient dynamic.Interface, namespace, appName string) {
	vpaGVR := schema.GroupVersionResource{
		Group:    "autoscaling.k8s.io",
		Version:  "v1",
		Resource: "verticalpodautoscalers",
	}

	vpaName := fmt.Sprintf("%s-vpa", appName)
	vpa, err := dynamicClient.Resource(vpaGVR).Namespace(namespace).Get(context.Background(), vpaName, metav1.GetOptions{})
	assert.NoError(s.T(), err)
	assert.NotNil(s.T(), vpa)
	assert.Equal(s.T(), vpaName, vpa.GetName())
	assert.Equal(s.T(), namespace, vpa.GetNamespace())

	fmt.Printf("VPA resource %s created successfully\n", vpaName)
}
