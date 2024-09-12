package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// Get ECS task for the specified service
func getECSTask(svc *ecs.Client, clusterName, serviceName string) (string, string, error) {
	input := &ecs.ListTasksInput{
		Cluster:       aws.String(clusterName),
		ServiceName:   aws.String(serviceName),
		DesiredStatus: types.DesiredStatusRunning, // Use the correct DesiredStatus enum
	}
	result, err := svc.ListTasks(context.TODO(), input)
	if err != nil || len(result.TaskArns) == 0 {
		return "", "", fmt.Errorf("no running tasks found for service %s", serviceName)
	}
	taskArn := result.TaskArns[0]

	// Describe the task to get container instance ARN
	describeInput := &ecs.DescribeTasksInput{
		Cluster: aws.String(clusterName),
		Tasks:   []string{taskArn},
	}
	describeResult, err := svc.DescribeTasks(context.TODO(), describeInput)
	if err != nil || len(describeResult.Tasks) == 0 {
		return "", "", fmt.Errorf("could not describe the ECS task")
	}
	containerInstanceArn := describeResult.Tasks[0].ContainerInstanceArn
	return taskArn, *containerInstanceArn, nil
}

// Get the EC2 instance ID from the container instance ARN
func getEC2InstanceID(svc *ecs.Client, clusterName, containerInstanceArn string) (string, error) {
	input := &ecs.DescribeContainerInstancesInput{
		Cluster:            aws.String(clusterName),
		ContainerInstances: []string{containerInstanceArn},
	}
	result, err := svc.DescribeContainerInstances(context.TODO(), input)
	if err != nil || len(result.ContainerInstances) == 0 {
		return "", fmt.Errorf("could not describe container instance")
	}
	return *result.ContainerInstances[0].Ec2InstanceId, nil
}

// Get the container ID for the specified container name
func getContainerID(svc *ecs.Client, clusterName, taskArn, containerName string) (string, error) {
	describeInput := &ecs.DescribeTasksInput{
		Cluster: aws.String(clusterName),
		Tasks:   []string{taskArn},
	}
	describeResult, err := svc.DescribeTasks(context.TODO(), describeInput)
	if err != nil || len(describeResult.Tasks) == 0 {
		return "", fmt.Errorf("could not describe the ECS task")
	}
	for _, container := range describeResult.Tasks[0].Containers {
		if *container.Name == containerName {
			return *container.RuntimeId, nil
		}
	}
	return "", fmt.Errorf("no container named %s found in task", containerName)
}

// Start an SSM session to connect to the container
func startSSMSession(instanceID, containerID string, profile *string, region string) error {
	ssmCmd := []string{
		"aws", "ssm", "start-session",
		"--target", instanceID,
		"--document-name", "AWS-StartInteractiveCommand",
		"--parameters", fmt.Sprintf("command=\"sudo docker exec -it %s bash\"", containerID),
		"--region", region, // Add region explicitly
	}
	if profile != nil {
		ssmCmd = append(ssmCmd, "--profile", *profile)
	}
	cmd := exec.Command(ssmCmd[0], ssmCmd[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func main() {
	// Define command-line flags
	clusterName := flag.String("cluster", "", "The ECS cluster name")
	serviceName := flag.String("service", "", "The ECS service name")
	containerName := flag.String("container", "", "The container name")
	profile := flag.String("profile", "", "Optional AWS profile name")

	flag.Parse()

	// Check required flags
	if *serviceName == "" || *containerName == "" {
		log.Fatal("Usage: go run main.go --cluster <cluster-name> --service <service-name> --container <container-name> [--profile <aws-profile>]")
	}

	// Load AWS config with region set to eu-west-2
	var cfg aws.Config
	var err error
	region := "eu-west-2" // Set the region here
	if *profile != "" {
		cfg, err = config.LoadDefaultConfig(context.TODO(), config.WithSharedConfigProfile(*profile), config.WithRegion(region))
	} else {
		cfg, err = config.LoadDefaultConfig(context.TODO(), config.WithRegion(region))
	}
	if err != nil {
		log.Fatalf("Unable to load AWS config: %v", err)
	}

	// Create ECS client
	ecsClient := ecs.NewFromConfig(cfg)

	// Get ECS task and container instance ARN
	taskArn, containerInstanceArn, err := getECSTask(ecsClient, *clusterName, *serviceName)
	if err != nil {
		log.Fatalf("Error getting ECS task: %v", err)
	}
	fmt.Printf("Found task ARN: %s\n", taskArn)

	// Get the EC2 instance ID
	instanceID, err := getEC2InstanceID(ecsClient, *clusterName, containerInstanceArn)
	if err != nil {
		log.Fatalf("Error getting EC2 instance ID: %v", err)
	}
	fmt.Printf("Found EC2 instance ID: %s\n", instanceID)

	// Get the container ID
	containerID, err := getContainerID(ecsClient, *clusterName, taskArn, *containerName)
	if err != nil {
		log.Fatalf("Error getting container ID: %v", err)
	}
	fmt.Printf("Found container ID: %s\n", containerID)

	// Start SSM session to connect to the container
	err = startSSMSession(instanceID, containerID, profile, region)
	if err != nil {
		log.Fatalf("Error starting SSM session: %v", err)
	}
}
