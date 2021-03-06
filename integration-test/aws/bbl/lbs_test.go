package integration_test

import (
	"fmt"
	"net/url"
	"strings"

	"golang.org/x/crypto/ssh"

	integration "github.com/cloudfoundry/bosh-bootloader/integration-test"
	"github.com/cloudfoundry/bosh-bootloader/integration-test/actors"
	"github.com/cloudfoundry/bosh-bootloader/testhelpers"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("load balancer tests", func() {
	var (
		bbl     actors.BBL
		aws     actors.AWS
		bosh    actors.BOSH
		boshcli actors.BOSHCLI
		state   integration.State
	)

	BeforeEach(func() {
		var err error
		configuration, err := integration.LoadConfig()
		Expect(err).NotTo(HaveOccurred())

		bbl = actors.NewBBL(configuration.StateFileDir, pathToBBL, configuration, "lbs-env")
		aws = actors.NewAWS(configuration)
		bosh = actors.NewBOSH()
		boshcli = actors.NewBOSHCLI()
		state = integration.NewState(configuration.StateFileDir)

	})

	It("creates, updates and deletes an LB with the specified cert and key", func() {
		bbl.Up(actors.AWSIAAS, []string{"--name", bbl.PredefinedEnvID()})

		stackName := state.StackName()
		directorAddress := bbl.DirectorAddress()
		caCertPath := bbl.SaveDirectorCA()

		Expect(aws.StackExists(stackName)).To(BeTrue())
		Expect(aws.LoadBalancers(stackName)).To(BeEmpty())
		exists, err := boshcli.DirectorExists(directorAddress, caCertPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeTrue())

		privateKey, err := ssh.ParsePrivateKey([]byte(bbl.SSHKey()))
		Expect(err).NotTo(HaveOccurred())

		directorAddressURL, err := url.Parse(bbl.DirectorAddress())
		Expect(err).NotTo(HaveOccurred())

		address := fmt.Sprintf("%s:22", directorAddressURL.Hostname())
		_, err = ssh.Dial("tcp", address, &ssh.ClientConfig{
			User: "jumpbox",
			Auth: []ssh.AuthMethod{
				ssh.PublicKeys(privateKey),
			},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		})
		Expect(err).NotTo(HaveOccurred())

		natInstanceID := aws.GetPhysicalID(stackName, "NATInstance")
		Expect(natInstanceID).NotTo(BeEmpty())

		tags := aws.GetEC2InstanceTags(natInstanceID)
		Expect(tags["bbl-env-id"]).To(Equal(bbl.PredefinedEnvID()))

		certPath, err := testhelpers.WriteContentsToTempFile(testhelpers.BBL_CERT)
		Expect(err).NotTo(HaveOccurred())

		chainPath, err := testhelpers.WriteContentsToTempFile(testhelpers.BBL_CHAIN)
		Expect(err).NotTo(HaveOccurred())

		keyPath, err := testhelpers.WriteContentsToTempFile(testhelpers.BBL_KEY)
		Expect(err).NotTo(HaveOccurred())

		otherCertPath, err := testhelpers.WriteContentsToTempFile(testhelpers.OTHER_BBL_CERT)
		Expect(err).NotTo(HaveOccurred())

		otherKeyPath, err := testhelpers.WriteContentsToTempFile(testhelpers.OTHER_BBL_KEY)
		Expect(err).NotTo(HaveOccurred())

		bbl.CreateLB("concourse", certPath, keyPath, chainPath)

		Expect(aws.LoadBalancers(stackName)).To(HaveKey("ConcourseLoadBalancer"))
		Expect(strings.TrimSpace(aws.DescribeCertificate(state.CertificateName()).Body)).To(Equal(strings.TrimSpace(testhelpers.BBL_CERT)))

		bbl.UpdateLB(otherCertPath, otherKeyPath)
		Expect(aws.LoadBalancers(stackName)).To(HaveKey("ConcourseLoadBalancer"))

		certificateName := state.CertificateName()
		Expect(strings.TrimSpace(aws.DescribeCertificate(certificateName).Body)).To(Equal(strings.TrimSpace(string(testhelpers.OTHER_BBL_CERT))))

		session := bbl.LBs()
		stdout := session.Out.Contents()
		Expect(stdout).To(ContainSubstring(fmt.Sprintf("Concourse LB: %s", aws.LoadBalancers(stackName)["ConcourseLoadBalancer"])))

		bbl.DeleteLBs()
		Expect(aws.LoadBalancers(stackName)).NotTo(HaveKey("ConcourseLoadBalancer"))
		Expect(strings.TrimSpace(aws.DescribeCertificate(certificateName).Body)).To(BeEmpty())

		bbl.Destroy()

		exists, _ = boshcli.DirectorExists(directorAddress, caCertPath)
		Expect(exists).To(BeFalse())

		Expect(aws.StackExists(stackName)).To(BeFalse())
	})
})
