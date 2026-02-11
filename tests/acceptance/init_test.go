package acceptance_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/re-cinq/claudit/tests/acceptance/testutil"
)

var _ = Describe("Init Command", func() {
	for _, cfg := range testutil.AllAgentConfigs() {
		config := cfg // capture loop variable

		Describe(config.Name+" agent", func() {
			var repo *testutil.GitRepo

			BeforeEach(func() {
				var err error
				repo, err = testutil.NewGitRepo()
				Expect(err).NotTo(HaveOccurred())

				if config.NeedsBinaryPath {
					repo.SetBinaryPath(testutil.BinaryPath())
				}
			})

			AfterEach(func() {
				if repo != nil {
					repo.Cleanup()
				}
			})

			It("creates settings file at correct path", func() {
				_, _, err := testutil.RunClauditInDir(repo.Path, config.InitArgs...)
				Expect(err).NotTo(HaveOccurred())

				Expect(repo.FileExists(config.SettingsFile)).To(BeTrue())
			})

			if config.IsPluginBased {
				It("creates plugin with correct store command and commit detection", func() {
					_, _, err := testutil.RunClauditInDir(repo.Path, config.InitArgs...)
					Expect(err).NotTo(HaveOccurred())

					content, err := repo.ReadFile(config.SettingsFile)
					Expect(err).NotTo(HaveOccurred())

					Expect(content).To(ContainSubstring(config.StoreCommand))
					Expect(content).To(ContainSubstring("git commit"))
				})

				It("is idempotent - same content on re-init", func() {
					_, _, err := testutil.RunClauditInDir(repo.Path, config.InitArgs...)
					Expect(err).NotTo(HaveOccurred())

					content1, err := repo.ReadFile(config.SettingsFile)
					Expect(err).NotTo(HaveOccurred())

					_, _, err = testutil.RunClauditInDir(repo.Path, config.InitArgs...)
					Expect(err).NotTo(HaveOccurred())

					content2, err := repo.ReadFile(config.SettingsFile)
					Expect(err).NotTo(HaveOccurred())

					Expect(content1).To(Equal(content2))
				})

				It("preserves other files in plugin directory", func() {
					pluginDir := filepath.Dir(filepath.Join(repo.Path, config.SettingsFile))
					Expect(os.MkdirAll(pluginDir, 0755)).To(Succeed())
					otherPlugin := filepath.Join(pluginDir, "other-plugin.js")
					Expect(os.WriteFile(otherPlugin, []byte("// other plugin"), 0644)).To(Succeed())

					_, _, err := testutil.RunClauditInDir(repo.Path, config.InitArgs...)
					Expect(err).NotTo(HaveOccurred())

					content, err := os.ReadFile(otherPlugin)
					Expect(err).NotTo(HaveOccurred())
					Expect(string(content)).To(Equal("// other plugin"))
				})
			} else {
				It("configures correct hook with right matcher/timeout/command", func() {
					_, _, err := testutil.RunClauditInDir(repo.Path, config.InitArgs...)
					Expect(err).NotTo(HaveOccurred())

					content, err := repo.ReadFile(config.SettingsFile)
					Expect(err).NotTo(HaveOccurred())

					var raw map[string]interface{}
					Expect(json.Unmarshal([]byte(content), &raw)).To(Succeed())

					hooks, ok := raw["hooks"].(map[string]interface{})
					Expect(ok).To(BeTrue(), "Expected hooks key in settings")

					hookArray, ok := hooks[config.HookKey].([]interface{})
					Expect(ok).To(BeTrue(), "Expected %s array", config.HookKey)
					Expect(hookArray).NotTo(BeEmpty())

					hookObj := hookArray[0].(map[string]interface{})
					Expect(hookObj["matcher"]).To(Equal(config.ToolMatcher))

					hookCmds := hookObj["hooks"].([]interface{})
					Expect(hookCmds).NotTo(BeEmpty())
					hookCmd := hookCmds[0].(map[string]interface{})
					Expect(hookCmd["command"]).To(Equal(config.StoreCommand))
					Expect(hookCmd["timeout"]).To(BeNumerically("==", config.Timeout))
				})

				It("is idempotent - no duplicate hooks on re-init", func() {
					_, _, err := testutil.RunClauditInDir(repo.Path, config.InitArgs...)
					Expect(err).NotTo(HaveOccurred())
					_, _, err = testutil.RunClauditInDir(repo.Path, config.InitArgs...)
					Expect(err).NotTo(HaveOccurred())

					content, err := repo.ReadFile(config.SettingsFile)
					Expect(err).NotTo(HaveOccurred())

					var raw map[string]interface{}
					Expect(json.Unmarshal([]byte(content), &raw)).To(Succeed())

					hooks := raw["hooks"].(map[string]interface{})
					hookArray := hooks[config.HookKey].([]interface{})
					Expect(hookArray).To(HaveLen(1))
				})

				It("preserves existing settings", func() {
					// Create pre-existing settings
					settingsDir := filepath.Dir(filepath.Join(repo.Path, config.SettingsFile))
					Expect(os.MkdirAll(settingsDir, 0755)).To(Succeed())
					Expect(repo.WriteFile(config.SettingsFile, `{"existingKey": "existingValue"}`)).To(Succeed())

					_, _, err := testutil.RunClauditInDir(repo.Path, config.InitArgs...)
					Expect(err).NotTo(HaveOccurred())

					content, err := repo.ReadFile(config.SettingsFile)
					Expect(err).NotTo(HaveOccurred())
					Expect(content).To(ContainSubstring("existingKey"))
					Expect(content).To(ContainSubstring("existingValue"))
					Expect(content).To(ContainSubstring(config.StoreCommand))
				})
			}

			if config.HasSessionHooks {
				It("creates session hooks with correct timeouts", func() {
					_, _, err := testutil.RunClauditInDir(repo.Path, config.InitArgs...)
					Expect(err).NotTo(HaveOccurred())

					content, err := repo.ReadFile(config.SettingsFile)
					Expect(err).NotTo(HaveOccurred())

					var raw map[string]interface{}
					Expect(json.Unmarshal([]byte(content), &raw)).To(Succeed())

					hooks := raw["hooks"].(map[string]interface{})

					for _, hookName := range []string{"SessionStart", "SessionEnd"} {
						hookArray := hooks[hookName].([]interface{})
						Expect(hookArray).NotTo(BeEmpty(), "Expected %s hook", hookName)
						hookObj := hookArray[0].(map[string]interface{})
						hookCmds := hookObj["hooks"].([]interface{})
						hookCmd := hookCmds[0].(map[string]interface{})
						Expect(hookCmd["timeout"]).To(BeNumerically("==", config.SessionTimeout),
							"Expected %s timeout to be %v", hookName, config.SessionTimeout)
					}
				})
			}
		})
	}

	// Shared tests that run once (not per-agent)
	Describe("shared behavior", func() {
		var repo *testutil.GitRepo

		BeforeEach(func() {
			var err error
			repo, err = testutil.NewGitRepo()
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			if repo != nil {
				repo.Cleanup()
			}
		})

		It("installs git hooks", func() {
			stdout, _, err := testutil.RunClauditInDir(repo.Path, "init")
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(ContainSubstring("Installed git hooks"))

			for _, hook := range []string{"pre-push", "post-merge", "post-checkout"} {
				hookPath := repo.Path + "/.git/hooks/" + hook
				info, err := os.Stat(hookPath)
				Expect(err).NotTo(HaveOccurred())
				Expect(info.Mode() & 0111).NotTo(BeZero()) // executable
			}
		})

		It("fails outside git repository", func() {
			tmpDir, err := os.MkdirTemp("", "not-a-repo-*")
			Expect(err).NotTo(HaveOccurred())
			defer os.RemoveAll(tmpDir)

			_, stderr, err := testutil.RunClauditInDir(tmpDir, "init")
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("not inside a git repository"))
		})
	})
})
