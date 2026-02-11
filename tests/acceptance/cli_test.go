package acceptance_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/re-cinq/claudit/tests/acceptance/testutil"
)

var _ = Describe("CLI Foundation", func() {
	Describe("claudit with no arguments", func() {
		It("displays help text", func() {
			stdout, _, err := testutil.RunClaudit()
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(ContainSubstring("Usage:"))
			Expect(stdout).To(ContainSubstring("Commands for humans:"))
		})
	})

	Describe("claudit --version", func() {
		It("displays the version number", func() {
			stdout, _, err := testutil.RunClaudit("--version")
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(ContainSubstring("claudit version"))
		})
	})

	Describe("claudit --help", func() {
		It("displays help text", func() {
			stdout, _, err := testutil.RunClaudit("--help")
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(ContainSubstring("Usage:"))
			Expect(stdout).To(ContainSubstring("init"))
			Expect(stdout).To(ContainSubstring("store"))
			Expect(stdout).To(ContainSubstring("sync"))
		})
	})
})
