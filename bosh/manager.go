package bosh

import (
	"errors"
	"fmt"
	"strings"

	yaml "gopkg.in/yaml.v2"

	"github.com/cloudfoundry/bosh-bootloader/aws/cloudformation"
	"github.com/cloudfoundry/bosh-bootloader/storage"
)

const (
	DIRECTOR_USERNAME = "admin"
)

type Manager struct {
	executor         executor
	terraformManager terraformManager
	stackManager     stackManager
	logger           logger
}

type directorOutputs struct {
	directorPassword       string
	directorSSLCA          string
	directorSSLCertificate string
	directorSSLPrivateKey  string
}

type deploymentVariables struct {
	DirectorName          string
	Zone                  string
	Network               string
	Subnetwork            string
	Tags                  []string
	ProjectID             string
	ExternalIP            string
	CredentialsJSON       string
	PrivateKey            string
	DefaultKeyName        string
	DefaultSecurityGroups []string
	SubnetID              string
	AZ                    string
	Region                string
	SecretAccessKey       string
	AccessKeyID           string
}

type iaasInputs struct {
	InterpolateInput InterpolateInput
	DirectorAddress  string
}

type executor interface {
	Interpolate(InterpolateInput) (InterpolateOutput, error)
	JumpboxInterpolate(InterpolateInput) (InterpolateOutput, error)
	CreateEnv(CreateEnvInput) (CreateEnvOutput, error)
	DeleteEnv(DeleteEnvInput) error
	Version() (string, error)
}

type terraformManager interface {
	GetOutputs(storage.State) (map[string]interface{}, error)
}

type stackManager interface {
	Describe(stackName string) (cloudformation.Stack, error)
}

type logger interface {
	Step(string, ...interface{})
}

func NewManager(executor executor, terraformManager terraformManager, stackManager stackManager, logger logger) Manager {
	return Manager{
		executor:         executor,
		terraformManager: terraformManager,
		stackManager:     stackManager,
		logger:           logger,
	}
}

func (m Manager) Version() (string, error) {
	return m.executor.Version()
}

func (m Manager) Create(state storage.State) (storage.State, error) {
	iaasInputs, err := m.generateIAASInputs(state)
	if err != nil {
		return storage.State{}, err
	}

	if state.Jumpbox {
		m.logger.Step("creating jumpbox")
		iaasInputs.InterpolateInput.JumpboxDeploymentVars, err = m.GetJumpboxDeploymentVars(state)
		if err != nil {
			//not tested
			return storage.State{}, err
		}

		interpolateOutputs, err := m.executor.JumpboxInterpolate(iaasInputs.InterpolateInput)
		if err != nil {
			return storage.State{}, err
		}

		variables, err := yaml.Marshal(interpolateOutputs.Variables)
		_, err = m.executor.CreateEnv(CreateEnvInput{
			Manifest:  interpolateOutputs.Manifest,
			Variables: string(variables),
		})
		switch err.(type) {
		case CreateEnvError:
			ceErr := err.(CreateEnvError)
			state.JumpboxDeployment = storage.JumpboxDeployment{
				Variables: string(variables),
				State:     ceErr.BOSHState(),
				Manifest:  interpolateOutputs.Manifest,
			}
			return storage.State{}, NewManagerCreateError(state, err)
		case error:
			return storage.State{}, err
		}
		m.logger.Step("created jumpbox")
	}

	m.logger.Step("setup proxy")
	// TODO: Setup socks5 proxy

	m.logger.Step("creating bosh director")
	iaasInputs.InterpolateInput.DeploymentVars, err = m.GetDeploymentVars(state)
	if err != nil {
		//not tested
		return storage.State{}, err
	}

	iaasInputs.InterpolateInput.OpsFile = state.BOSH.UserOpsFile

	interpolateOutputs, err := m.executor.Interpolate(iaasInputs.InterpolateInput)
	if err != nil {
		return storage.State{}, err
	}

	variables, err := yaml.Marshal(interpolateOutputs.Variables)
	createEnvOutputs, err := m.executor.CreateEnv(CreateEnvInput{
		Manifest:  interpolateOutputs.Manifest,
		State:     state.BOSH.State,
		Variables: string(variables),
	})
	switch err.(type) {
	case CreateEnvError:
		ceErr := err.(CreateEnvError)
		state.BOSH = storage.BOSH{
			Variables: string(variables),
			State:     ceErr.BOSHState(),
			Manifest:  interpolateOutputs.Manifest,
		}
		return storage.State{}, NewManagerCreateError(state, err)
	case error:
		return storage.State{}, err
	}

	directorOutputs := getDirectorOutputs(interpolateOutputs.Variables)

	state.BOSH = storage.BOSH{
		DirectorName:           fmt.Sprintf("bosh-%s", state.EnvID),
		DirectorAddress:        iaasInputs.DirectorAddress,
		DirectorUsername:       DIRECTOR_USERNAME,
		DirectorPassword:       directorOutputs.directorPassword,
		DirectorSSLCA:          directorOutputs.directorSSLCA,
		DirectorSSLCertificate: directorOutputs.directorSSLCertificate,
		DirectorSSLPrivateKey:  directorOutputs.directorSSLPrivateKey,
		Variables:              string(variables),
		State:                  createEnvOutputs.State,
		Manifest:               interpolateOutputs.Manifest,
	}

	m.logger.Step("created bosh director")
	return state, nil
}

func (m Manager) Delete(state storage.State) error {
	err := m.executor.DeleteEnv(DeleteEnvInput{
		Manifest:  state.BOSH.Manifest,
		State:     state.BOSH.State,
		Variables: state.BOSH.Variables,
	})
	switch err.(type) {
	case DeleteEnvError:
		deErr := err.(DeleteEnvError)
		state.BOSH.State = deErr.BOSHState()
		return NewManagerDeleteError(state, err)
	case error:
		return err
	}

	return nil
}

func (m Manager) GetJumpboxDeploymentVars(state storage.State) (string, error) {
	terraformOutputs, err := m.terraformManager.GetOutputs(state)
	if err != nil {
		return "", err
	}

	vars := strings.Join([]string{
		"internal_cidr: 10.0.0.0/24",
		"internal_gw: 10.0.0.1",
		"internal_ip: 10.0.0.5",
		fmt.Sprintf("director_name: %s", fmt.Sprintf("bosh-%s", state.EnvID)),
		fmt.Sprintf("external_ip: %s", terraformOutputs["external_ip"]),
		fmt.Sprintf("zone: %s", state.GCP.Zone),
		fmt.Sprintf("network: %s", terraformOutputs["network_name"]),
		fmt.Sprintf("subnetwork: %s", terraformOutputs["subnetwork_name"]),
		fmt.Sprintf("tags: [%s, %s]", terraformOutputs["bosh_open_tag_name"], terraformOutputs["internal_tag_name"]),
		fmt.Sprintf("project_id: %s", state.GCP.ProjectID),
		fmt.Sprintf("gcp_credentials_json: '%s'", state.GCP.ServiceAccountKey),
	}, "\n")

	return strings.TrimSuffix(vars, "\n"), nil
}

func (m Manager) GetDeploymentVars(state storage.State) (string, error) {
	var vars string

	switch state.IAAS {
	case "gcp":
		terraformOutputs, err := m.terraformManager.GetOutputs(state)
		if err != nil {
			return "", err
		}

		if state.Jumpbox {
			vars = strings.Join([]string{
				"internal_cidr: 10.0.0.0/24",
				"internal_gw: 10.0.0.1",
				"internal_ip: 10.0.0.6",
				fmt.Sprintf("director_name: %s", fmt.Sprintf("bosh-%s", state.EnvID)),
				fmt.Sprintf("zone: %s", state.GCP.Zone),
				fmt.Sprintf("network: %s", terraformOutputs["network_name"]),
				fmt.Sprintf("subnetwork: %s", terraformOutputs["subnetwork_name"]),
				fmt.Sprintf("tags: [%s]", terraformOutputs["internal_tag_name"]),
				fmt.Sprintf("project_id: %s", state.GCP.ProjectID),
				fmt.Sprintf("gcp_credentials_json: '%s'", state.GCP.ServiceAccountKey),
			}, "\n")
		} else {
			vars = strings.Join([]string{
				"internal_cidr: 10.0.0.0/24",
				"internal_gw: 10.0.0.1",
				"internal_ip: 10.0.0.6",
				fmt.Sprintf("director_name: %s", fmt.Sprintf("bosh-%s", state.EnvID)),
				fmt.Sprintf("external_ip: %s", terraformOutputs["external_ip"]),
				fmt.Sprintf("zone: %s", state.GCP.Zone),
				fmt.Sprintf("network: %s", terraformOutputs["network_name"]),
				fmt.Sprintf("subnetwork: %s", terraformOutputs["subnetwork_name"]),
				fmt.Sprintf("tags: [%s, %s]", terraformOutputs["bosh_open_tag_name"], terraformOutputs["internal_tag_name"]),
				fmt.Sprintf("project_id: %s", state.GCP.ProjectID),
				fmt.Sprintf("gcp_credentials_json: '%s'", state.GCP.ServiceAccountKey),
			}, "\n")
		}
	case "aws":
		if state.TFState != "" {
			terraformOutputs, err := m.terraformManager.GetOutputs(state)
			if err != nil {
				return "", err
			}
			vars = strings.Join([]string{
				"internal_cidr: 10.0.0.0/24",
				"internal_gw: 10.0.0.1",
				"internal_ip: 10.0.0.6",
				fmt.Sprintf("director_name: %s", fmt.Sprintf("bosh-%s", state.EnvID)),
				fmt.Sprintf("external_ip: %s", terraformOutputs["external_ip"]),
				fmt.Sprintf("az: %s", terraformOutputs["az"]),
				fmt.Sprintf("subnet_id: %s", terraformOutputs["subnet_id"]),
				fmt.Sprintf("access_key_id: %s", terraformOutputs["access_key_id"]),
				fmt.Sprintf("secret_access_key: %s", terraformOutputs["secret_access_key"]),
				fmt.Sprintf("default_key_name: %s", state.KeyPair.Name),
				fmt.Sprintf("default_security_groups: [%s]", terraformOutputs["default_security_groups"]),
				fmt.Sprintf("region: %s", state.AWS.Region),
				fmt.Sprintf("private_key: |-\n  %s", strings.Replace(state.KeyPair.PrivateKey, "\n", "\n  ", -1)),
			}, "\n")
		} else {
			stack, err := m.stackManager.Describe(state.Stack.Name)
			if err != nil {
				return "", err
			}
			vars = strings.Join([]string{
				"internal_cidr: 10.0.0.0/24",
				"internal_gw: 10.0.0.1",
				"internal_ip: 10.0.0.6",
				fmt.Sprintf("director_name: %s", fmt.Sprintf("bosh-%s", state.EnvID)),
				fmt.Sprintf("external_ip: %s", stack.Outputs["BOSHEIP"]),
				fmt.Sprintf("az: %s", stack.Outputs["BOSHSubnetAZ"]),
				fmt.Sprintf("subnet_id: %s", stack.Outputs["BOSHSubnet"]),
				fmt.Sprintf("access_key_id: %s", stack.Outputs["BOSHUserAccessKey"]),
				fmt.Sprintf("secret_access_key: %s", stack.Outputs["BOSHUserSecretAccessKey"]),
				fmt.Sprintf("default_key_name: %s", state.KeyPair.Name),
				fmt.Sprintf("default_security_groups: [%s]", stack.Outputs["BOSHSecurityGroup"]),
				fmt.Sprintf("region: %s", state.AWS.Region),
				fmt.Sprintf("private_key: |-\n  %s", strings.Replace(state.KeyPair.PrivateKey, "\n", "\n  ", -1)),
			}, "\n")
		}
	}

	return strings.TrimSuffix(vars, "\n"), nil
}

func (m Manager) generateIAASInputs(state storage.State) (iaasInputs, error) {
	switch state.IAAS {
	case "gcp":
		terraformOutputs, err := m.terraformManager.GetOutputs(state)
		if err != nil {
			return iaasInputs{}, err
		}
		return iaasInputs{
			InterpolateInput: InterpolateInput{
				IAAS:      state.IAAS,
				BOSHState: state.BOSH.State,
				Variables: state.BOSH.Variables,
			},
			DirectorAddress: terraformOutputs["director_address"].(string),
		}, nil
	case "aws":
		if state.TFState != "" {
			terraformOutputs, err := m.terraformManager.GetOutputs(state)
			if err != nil {
				return iaasInputs{}, err
			}
			return iaasInputs{
				InterpolateInput: InterpolateInput{
					IAAS:      state.IAAS,
					BOSHState: state.BOSH.State,
					Variables: state.BOSH.Variables,
				},
				DirectorAddress: terraformOutputs["director_address"].(string),
			}, nil
		} else {
			stack, err := m.stackManager.Describe(state.Stack.Name)
			if err != nil {
				return iaasInputs{}, err
			}
			return iaasInputs{
				InterpolateInput: InterpolateInput{
					IAAS:      state.IAAS,
					BOSHState: state.BOSH.State,
					Variables: state.BOSH.Variables,
				},
				DirectorAddress: stack.Outputs["BOSHURL"],
			}, nil
		}
	default:
		return iaasInputs{}, errors.New("A valid IAAS was not provided")
	}
}

func getDirectorOutputs(variables map[interface{}]interface{}) directorOutputs {
	directorSSLInterfaceMap := variables["director_ssl"].(map[interface{}]interface{})
	directorSSL := map[string]string{}
	for k, v := range directorSSLInterfaceMap {
		directorSSL[k.(string)] = v.(string)
	}

	return directorOutputs{
		directorPassword:       variables["admin_password"].(string),
		directorSSLCA:          directorSSL["ca"],
		directorSSLCertificate: directorSSL["certificate"],
		directorSSLPrivateKey:  directorSSL["private_key"],
	}
}
