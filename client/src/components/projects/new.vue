<template>
  <v-container fluid>
    <v-row class="mb-4">
      <v-col>
        <div class="text-h5">New Project</div>
        <div class="text-body-2 text-medium-emphasis">
          Connect a GitHub repository. kuso will detect the runtime and create
          a service for it. Add more services and addons after.
        </div>
      </v-col>
    </v-row>

    <v-row>
      <v-col cols="12" md="8">
        <v-card variant="outlined" class="pa-6">
          <v-form @submit.prevent="submit">
            <!-- GitHub source -->
            <v-card-subtitle class="px-0 mb-2">Source</v-card-subtitle>

            <div v-if="!githubReady" class="mb-6">
              <v-alert
                type="info"
                variant="tonal"
                class="mb-4"
              >
                <div class="d-flex align-center justify-space-between flex-wrap gap-2">
                  <div>
                    <div class="text-subtitle-2">GitHub App not connected</div>
                    <div class="text-caption">
                      Connect the kuso GitHub App to pick repos visually.
                      Or paste a repo URL below.
                    </div>
                  </div>
                  <v-btn
                    v-if="installUrl"
                    size="small"
                    color="primary"
                    :href="installUrl"
                    target="_blank"
                    append-icon="mdi-open-in-new"
                  >
                    Install GitHub App
                  </v-btn>
                </div>
              </v-alert>
              <v-text-field
                v-model="manualRepoUrl"
                label="Repo URL"
                placeholder="https://github.com/org/repo"
                :rules="[required('Repo URL is required')]"
                density="comfortable"
              />
            </div>

            <div v-else class="mb-6">
              <v-select
                v-model="selectedInstallationId"
                :items="installations"
                :item-title="installationLabel"
                item-value="id"
                label="GitHub account"
                density="comfortable"
                @update:model-value="loadInstallationRepos"
              />
              <v-autocomplete
                v-model="selectedRepo"
                :items="installationRepos"
                :item-title="(r: any) => r.fullName"
                return-object
                label="Repository"
                :loading="loadingRepos"
                density="comfortable"
                clearable
              />
            </div>

            <!-- Project metadata -->
            <v-card-subtitle class="px-0 mb-2">Project</v-card-subtitle>
            <v-text-field
              v-model="name"
              label="Name"
              :hint="nameHint"
              persistent-hint
              :rules="[required('Name is required'), nameValid]"
              density="comfortable"
            />
            <v-text-field
              v-model="defaultBranch"
              label="Default branch"
              :rules="[required('Branch is required')]"
              density="comfortable"
            />
            <v-text-field
              v-model="baseDomain"
              label="Base domain"
              hint="Services get auto-generated <service>.<this>. Leave blank for the cluster default."
              persistent-hint
              density="comfortable"
            />

            <!-- Previews -->
            <v-card-subtitle class="px-0 mt-4 mb-2">Preview deployments</v-card-subtitle>
            <v-switch
              v-model="previewsEnabled"
              label="Spin up a preview environment for every pull request"
              :disabled="!githubReady"
              :messages="!githubReady
                ? 'Requires the GitHub App'
                : 'Preview envs are torn down when the PR closes'"
              color="primary"
              density="comfortable"
            />

            <!-- Submit -->
            <v-divider class="my-4" />
            <div class="d-flex align-center">
              <v-spacer />
              <v-btn variant="text" @click="$router.push('/')">Cancel</v-btn>
              <v-btn
                color="primary"
                :loading="submitting"
                :disabled="!canSubmit"
                @click="submit"
              >
                Create project
              </v-btn>
            </div>
            <v-alert v-if="error" type="error" variant="tonal" class="mt-4">
              {{ error }}
            </v-alert>
          </v-form>
        </v-card>
      </v-col>
    </v-row>
  </v-container>
</template>

<script lang="ts" setup>
import { computed, onMounted, ref, watch } from 'vue'
import axios from 'axios'
import { useRouter } from 'vue-router'

interface Repo {
  id: number
  name: string
  fullName: string
  private: boolean
  defaultBranch: string
}

interface Installation {
  id: number
  accountLogin: string
  accountType: string
  repositories: Repo[]
}

const router = useRouter()

const installUrl = ref<string | null>(null)
const githubReady = ref(false)
const installations = ref<Installation[]>([])
const installationRepos = ref<Repo[]>([])
const loadingRepos = ref(false)
const selectedInstallationId = ref<number | null>(null)
const selectedRepo = ref<Repo | null>(null)
const manualRepoUrl = ref('')

const name = ref('')
const defaultBranch = ref('main')
const baseDomain = ref('')
const previewsEnabled = ref(false)
const submitting = ref(false)
const error = ref('')

const nameHint = computed(() =>
  selectedRepo.value
    ? `Defaults to "${selectedRepo.value.name}" if blank`
    : 'Lower-case letters, numbers, and hyphens. Used for namespaces and DNS.',
)

const canSubmit = computed(() => {
  if (!name.value && !selectedRepo.value) return false
  if (!githubReady.value && !manualRepoUrl.value) return false
  return true
})

function required(msg: string) {
  return (v: string) => !!v || msg
}
function nameValid(v: string) {
  if (!v) return true
  return /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/.test(v) || 'lower-case alphanumerics + hyphens, must start and end with alphanumeric'
}

function installationLabel(i: Installation): string {
  return `${i.accountLogin}  (${i.accountType.toLowerCase()})`
}

async function loadGithubStatus() {
  try {
    const res = await axios.get('/api/github/install-url')
    installUrl.value = res.data?.url ?? null
    githubReady.value = !!res.data?.configured
  } catch {
    githubReady.value = false
  }
  if (githubReady.value) {
    await loadInstallations()
  }
}

async function loadInstallations() {
  try {
    const res = await axios.get('/api/github/installations')
    installations.value = res.data || []
    if (installations.value.length === 1) {
      selectedInstallationId.value = installations.value[0].id
      await loadInstallationRepos(selectedInstallationId.value)
    }
  } catch {
    installations.value = []
  }
}

async function loadInstallationRepos(installationId: number | null) {
  if (!installationId) return
  loadingRepos.value = true
  try {
    const res = await axios.get(`/api/github/installations/${installationId}/repos`)
    installationRepos.value = res.data || []
  } catch {
    installationRepos.value = []
  } finally {
    loadingRepos.value = false
  }
}

watch(selectedRepo, (r) => {
  if (r) {
    if (!name.value) name.value = r.name
    if (defaultBranch.value === 'main' && r.defaultBranch) {
      defaultBranch.value = r.defaultBranch
    }
  }
})

async function submit() {
  error.value = ''
  if (!canSubmit.value) return

  submitting.value = true
  try {
    const repoUrl = githubReady.value
      ? selectedRepo.value
        ? `https://github.com/${selectedRepo.value.fullName}`
        : ''
      : manualRepoUrl.value.trim()

    if (!repoUrl) {
      error.value = 'Repo URL is required'
      return
    }

    const projectName = name.value || selectedRepo.value?.name || ''
    if (!projectName) {
      error.value = 'Project name is required'
      return
    }

    await axios.post('/api/projects', {
      name: projectName,
      defaultRepo: { url: repoUrl, defaultBranch: defaultBranch.value },
      baseDomain: baseDomain.value || undefined,
      github: githubReady.value && selectedInstallationId.value
        ? { installationId: selectedInstallationId.value }
        : undefined,
      previews: { enabled: previewsEnabled.value },
    })
    router.push(`/projects/${projectName}`)
  } catch (e: any) {
    error.value = e?.response?.data?.message || e?.message || 'Failed to create project'
  } finally {
    submitting.value = false
  }
}

onMounted(loadGithubStatus)
</script>
