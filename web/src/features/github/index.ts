export {
  useInstallURL,
  useInstallations,
  useInstallationRepos,
  useGithubRepos,
  announceGithubInstalled,
  useDetectRuntime,
  useScanAddons,
  useSetupStatus,
  useConfigureGithub,
} from "./hooks";
export { getGithubManifest } from "./api";
export type {
  GithubInstallation,
  GithubRepo,
  GithubRepoRef,
  DetectRuntimeResponse,
  AddonSuggestion,
  SetupStatusResponse,
  ConfigureBody,
  ConfigureResponse,
  ManifestResponse,
} from "./api";
