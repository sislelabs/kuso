<template>
  <v-container fluid>
    <v-progress-linear v-if="loading" indeterminate />
    <template v-else-if="project">
      <v-row class="mb-4">
        <v-col>
          <div class="d-flex align-center">
            <v-btn icon="mdi-arrow-left" variant="text" @click="$router.push('/')" class="mr-2" />
            <div>
              <div class="text-h5">{{ project.metadata.name }}</div>
              <div class="text-body-2 text-medium-emphasis">
                <v-icon size="small" class="mr-1">mdi-source-branch</v-icon>
                {{ shortRepo(project.spec.defaultRepo?.url) }}
                <span v-if="project.spec.defaultRepo?.defaultBranch">
                   · {{ project.spec.defaultRepo.defaultBranch }}
                </span>
              </div>
            </div>
            <v-spacer />
            <v-chip v-if="project.spec.previews?.enabled" size="small" color="success" class="mr-2">
              Previews on
            </v-chip>
            <v-btn
              icon="mdi-delete"
              variant="text"
              color="error"
              @click="confirmDelete = true"
            />
          </div>
        </v-col>
      </v-row>

      <v-tabs v-model="tab">
        <v-tab value="services">
          Services
          <v-badge :content="services.length" inline color="primary" class="ml-2" />
        </v-tab>
        <v-tab value="environments">
          Environments
          <v-badge :content="environments.length" inline color="primary" class="ml-2" />
        </v-tab>
        <v-tab value="addons">
          Addons
          <v-badge :content="addons.length" inline color="primary" class="ml-2" />
        </v-tab>
        <v-tab value="deploys">
          Deploys
          <v-badge :content="builds.length" inline color="primary" class="ml-2" />
        </v-tab>
      </v-tabs>

      <v-window v-model="tab" class="mt-4">
        <!-- Services -->
        <v-window-item value="services">
          <div class="d-flex justify-end mb-3">
            <v-btn color="primary" prepend-icon="mdi-plus" @click="showAddService = true">
              Add Service
            </v-btn>
          </div>
          <v-card v-if="services.length === 0" variant="outlined" class="pa-8 text-center">
            <v-icon size="48" color="primary" class="mb-3">mdi-package-variant</v-icon>
            <div class="text-subtitle-1 mb-2">No services yet</div>
            <div class="text-body-2 text-medium-emphasis mb-4">
              Add a service from this project's repo to deploy something.
            </div>
            <v-btn color="primary" @click="showAddService = true">Add the first service</v-btn>
          </v-card>
          <v-row v-else>
            <v-col v-for="s in services" :key="s.metadata.name" cols="12" md="6">
              <v-card variant="outlined">
                <v-card-item>
                  <v-card-title>{{ shortServiceName(s.metadata.name) }}</v-card-title>
                  <v-card-subtitle>
                    {{ s.spec.runtime || 'unknown runtime' }}
                    · port {{ s.spec.port || '?' }}
                    <span v-if="s.spec.repo?.path && s.spec.repo.path !== '.'">
                      · path {{ s.spec.repo.path }}
                    </span>
                  </v-card-subtitle>
                </v-card-item>
                <v-card-text>
                  <div v-if="productionUrl(s)" class="text-caption">
                    <v-icon size="small" class="mr-1">mdi-link-variant</v-icon>
                    <a :href="productionUrl(s) || ''" target="_blank" rel="noopener">
                      {{ productionUrl(s) }}
                    </a>
                  </div>
                </v-card-text>
                <v-card-actions>
                  <v-btn variant="text" @click="redeploy(s)" color="primary">
                    Redeploy
                  </v-btn>
                  <v-spacer />
                  <v-btn variant="text" @click="deleteService(s)" color="error">
                    Delete
                  </v-btn>
                </v-card-actions>
              </v-card>
            </v-col>
          </v-row>
        </v-window-item>

        <!-- Environments -->
        <v-window-item value="environments">
          <v-card v-if="environments.length === 0" variant="outlined" class="pa-8 text-center">
            <div class="text-subtitle-1 mb-2">No environments yet</div>
            <div class="text-body-2 text-medium-emphasis">
              Production environments are created when you add a service.
              Preview envs spawn from pull requests when previews are on.
            </div>
          </v-card>
          <v-table v-else>
            <thead>
              <tr>
                <th>Environment</th>
                <th>Service</th>
                <th>Kind</th>
                <th>Branch</th>
                <th>URL</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="e in environments" :key="e.metadata.name">
                <td><code>{{ e.metadata.name }}</code></td>
                <td>{{ shortServiceName(e.spec.service) }}</td>
                <td>
                  <v-chip
                    size="x-small"
                    :color="e.spec.kind === 'production' ? 'primary' : 'warning'"
                  >
                    {{ e.spec.kind }}
                  </v-chip>
                </td>
                <td><code>{{ e.spec.branch }}</code></td>
                <td>
                  <a v-if="e.spec.host" :href="`https://${e.spec.host}`" target="_blank" rel="noopener">
                    {{ e.spec.host }}
                  </a>
                  <span v-else class="text-medium-emphasis">—</span>
                </td>
                <td>
                  <v-btn
                    v-if="e.spec.kind === 'preview'"
                    size="x-small"
                    variant="text"
                    color="error"
                    @click="deleteEnvironment(e)"
                  >Delete</v-btn>
                </td>
              </tr>
            </tbody>
          </v-table>
        </v-window-item>

        <!-- Addons -->
        <v-window-item value="addons">
          <div class="d-flex justify-end mb-3">
            <v-btn color="primary" prepend-icon="mdi-plus" @click="showAddAddon = true">
              Add Addon
            </v-btn>
          </div>
          <v-card v-if="addons.length === 0" variant="outlined" class="pa-8 text-center">
            <v-icon size="48" color="primary" class="mb-3">mdi-database-plus</v-icon>
            <div class="text-subtitle-1 mb-2">No addons yet</div>
            <div class="text-body-2 text-medium-emphasis mb-4">
              Add a database, cache, or queue. Connection info is auto-injected
              into every service in the project.
            </div>
            <v-btn color="primary" @click="showAddAddon = true">Add an addon</v-btn>
          </v-card>
          <v-row v-else>
            <v-col v-for="a in addons" :key="a.metadata.name" cols="12" md="6">
              <v-card variant="outlined">
                <v-card-item>
                  <v-card-title>{{ shortAddonName(a.metadata.name) }}</v-card-title>
                  <v-card-subtitle>
                    {{ a.spec.kind }} {{ a.spec.version || '' }}
                    · {{ a.spec.size || 'small' }}
                    <v-chip v-if="a.spec.ha" size="x-small" class="ml-1">HA</v-chip>
                  </v-card-subtitle>
                </v-card-item>
                <v-card-text>
                  <div class="text-caption">
                    Conn secret:
                    <code>{{ a.spec.project }}-{{ shortAddonName(a.metadata.name) }}-conn</code>
                  </div>
                </v-card-text>
                <v-card-actions>
                  <v-btn variant="text" @click="deleteAddon(a)" color="error">Delete</v-btn>
                </v-card-actions>
              </v-card>
            </v-col>
          </v-row>
        </v-window-item>

        <!-- Deploys -->
        <v-window-item value="deploys">
          <v-card v-if="builds.length === 0" variant="outlined" class="pa-8 text-center">
            <v-icon size="48" color="primary" class="mb-3">mdi-rocket-launch-outline</v-icon>
            <div class="text-subtitle-1 mb-2">No deploys yet</div>
            <div class="text-body-2 text-medium-emphasis">
              Push to your default branch, or click "Redeploy" on a service.
            </div>
          </v-card>
          <v-table v-else>
            <thead>
              <tr>
                <th>When</th>
                <th>Service</th>
                <th>Branch</th>
                <th>Commit</th>
                <th>Status</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="b in builds" :key="b.metadata.name">
                <td>{{ relativeTime(b.metadata.creationTimestamp) }}</td>
                <td>{{ shortServiceName(b.spec.service) }}</td>
                <td><code>{{ b.spec.branch || '-' }}</code></td>
                <td>
                  <code>{{ (b.spec.ref || '').slice(0, 12) }}</code>
                </td>
                <td>
                  <v-chip size="x-small" :color="phaseColor(b.status?.phase)">
                    {{ b.status?.phase || 'pending' }}
                  </v-chip>
                </td>
              </tr>
            </tbody>
          </v-table>
        </v-window-item>
      </v-window>

      <!-- Add Service Dialog -->
      <v-dialog v-model="showAddService" max-width="600" persistent>
        <AddServiceDialog
          :project-name="projectName"
          :installation-id="project.spec.github?.installationId"
          :default-repo="project.spec.defaultRepo?.url"
          :default-branch="project.spec.defaultRepo?.defaultBranch"
          @close="showAddService = false"
          @added="onServiceAdded"
        />
      </v-dialog>

      <!-- Add Addon Dialog -->
      <v-dialog v-model="showAddAddon" max-width="500" persistent>
        <AddAddonDialog
          :project-name="projectName"
          @close="showAddAddon = false"
          @added="onAddonAdded"
        />
      </v-dialog>

      <!-- Delete confirm -->
      <v-dialog v-model="confirmDelete" max-width="400">
        <v-card>
          <v-card-title>Delete project?</v-card-title>
          <v-card-text>
            Cascade-deletes every service, environment, and addon in
            <strong>{{ projectName }}</strong>. This cannot be undone.
          </v-card-text>
          <v-card-actions>
            <v-spacer />
            <v-btn variant="text" @click="confirmDelete = false">Cancel</v-btn>
            <v-btn color="error" :loading="deleting" @click="deleteProject">Delete</v-btn>
          </v-card-actions>
        </v-card>
      </v-dialog>
    </template>

    <v-alert v-else-if="!loading" type="error" variant="tonal">
      Project not found.
    </v-alert>
  </v-container>
</template>

<script lang="ts" setup>
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import axios from 'axios'
import AddServiceDialog from './add-service.vue'
import AddAddonDialog from './add-addon.vue'

interface ProjectDetail {
  project: any
  services: any[]
  environments: any[]
  addons: any[]
}

const route = useRoute()
const router = useRouter()

const projectName = computed(() => String(route.params.project))
const loading = ref(true)
const project = ref<any>(null)
const services = ref<any[]>([])
const environments = ref<any[]>([])
const addons = ref<any[]>([])
const builds = ref<any[]>([])
const tab = ref('services')

const showAddService = ref(false)
const showAddAddon = ref(false)
const confirmDelete = ref(false)
const deleting = ref(false)

async function load() {
  loading.value = true
  try {
    const res = await axios.get(`/api/projects/${projectName.value}`)
    const data = res.data as ProjectDetail
    project.value = data.project
    services.value = data.services || []
    environments.value = data.environments || []
    addons.value = data.addons || []
    await loadBuilds()
  } catch {
    project.value = null
  } finally {
    loading.value = false
  }
}

async function loadBuilds() {
  // Builds are listed per-service; fan out and merge.
  const all: any[] = []
  for (const s of services.value) {
    const short = shortServiceName(s.metadata.name)
    try {
      const r = await axios.get(
        `/api/projects/${projectName.value}/services/${short}/builds`,
      )
      const items = Array.isArray(r.data) ? r.data : []
      all.push(...items)
    } catch {
      // ignore — empty list on 404 etc.
    }
  }
  // Newest first
  all.sort((a, b) =>
    String(b.metadata.creationTimestamp || '').localeCompare(
      String(a.metadata.creationTimestamp || ''),
    ),
  )
  builds.value = all
}

async function redeploy(s: any) {
  const short = shortServiceName(s.metadata.name)
  try {
    await axios.post(`/api/projects/${projectName.value}/services/${short}/builds`, {})
    // Switch to deploys tab to show progress, then refresh
    tab.value = 'deploys'
    await loadBuilds()
  } catch (e: any) {
    alert(`Redeploy failed: ${e?.response?.data?.message || e?.message}`)
  }
}

function relativeTime(iso?: string): string {
  if (!iso) return '-'
  const d = Date.now() - Date.parse(iso)
  const s = Math.max(0, Math.floor(d / 1000))
  if (s < 60) return `${s}s ago`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ago`
  return `${Math.floor(h / 24)}d ago`
}

function phaseColor(phase?: string): string {
  switch (phase) {
    case 'succeeded':
      return 'success'
    case 'failed':
      return 'error'
    case 'running':
      return 'info'
    default:
      return 'warning'
  }
}

function shortRepo(url?: string): string {
  if (!url) return '(no repo)'
  const m = url.match(/github\.com[/:](.+?)(?:\.git)?$/)
  return m?.[1] || url
}

function shortServiceName(fqn: string): string {
  return fqn.startsWith(`${projectName.value}-`)
    ? fqn.slice(projectName.value.length + 1)
    : fqn
}

function shortAddonName(fqn: string): string {
  return fqn.startsWith(`${projectName.value}-`)
    ? fqn.slice(projectName.value.length + 1)
    : fqn
}

function productionUrl(svc: any): string | null {
  const env = environments.value.find(
    (e) => e.spec.service === svc.metadata.name && e.spec.kind === 'production',
  )
  return env?.spec?.host ? `https://${env.spec.host}` : null
}

async function deleteService(s: any) {
  const short = shortServiceName(s.metadata.name)
  if (!confirm(`Delete service "${short}"? Cascade-deletes all environments.`)) return
  await axios.delete(`/api/projects/${projectName.value}/services/${short}`)
  await load()
}

async function deleteAddon(a: any) {
  const short = shortAddonName(a.metadata.name)
  if (!confirm(`Delete addon "${short}"?`)) return
  await axios.delete(`/api/projects/${projectName.value}/addons/${short}`)
  await load()
}

async function deleteEnvironment(e: any) {
  if (!confirm(`Delete preview env "${e.metadata.name}"?`)) return
  await axios.delete(`/api/projects/${projectName.value}/envs/${e.metadata.name}`)
  await load()
}

async function deleteProject() {
  deleting.value = true
  try {
    await axios.delete(`/api/projects/${projectName.value}`)
    router.push('/')
  } finally {
    deleting.value = false
  }
}

function onServiceAdded() {
  showAddService.value = false
  load()
}
function onAddonAdded() {
  showAddAddon.value = false
  load()
}

onMounted(load)
</script>
