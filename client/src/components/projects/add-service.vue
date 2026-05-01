<template>
  <v-card>
    <v-card-title>Add a service</v-card-title>
    <v-card-text>
      <v-form @submit.prevent="submit">
        <v-text-field
          v-model="name"
          label="Name"
          hint="e.g. web, api, worker"
          :rules="[required('Name is required'), nameValid]"
          density="comfortable"
        />
        <v-text-field
          v-model="path"
          label="Path"
          hint="Subdirectory in the repo. Use '.' for the repo root."
          density="comfortable"
        />
        <div class="d-flex align-center mb-2">
          <v-btn
            v-if="props.installationId"
            size="small"
            variant="outlined"
            :loading="detecting"
            @click="detect"
          >
            Auto-detect runtime
          </v-btn>
          <v-spacer />
          <span v-if="detectionReason" class="text-caption text-medium-emphasis">
            {{ detectionReason }}
          </span>
        </div>
        <v-select
          v-model="runtime"
          :items="['dockerfile', 'nixpacks', 'buildpacks', 'static']"
          label="Runtime"
          density="comfortable"
        />
        <v-text-field
          v-model.number="port"
          label="Port"
          type="number"
          density="comfortable"
        />
        <v-alert v-if="error" type="error" variant="tonal" class="mt-2">
          {{ error }}
        </v-alert>
      </v-form>
    </v-card-text>
    <v-card-actions>
      <v-spacer />
      <v-btn variant="text" @click="$emit('close')">Cancel</v-btn>
      <v-btn color="primary" :loading="submitting" :disabled="!canSubmit" @click="submit">
        Add service
      </v-btn>
    </v-card-actions>
  </v-card>
</template>

<script lang="ts" setup>
import { computed, ref } from 'vue'
import axios from 'axios'

const props = defineProps<{
  projectName: string
  installationId?: number
  defaultRepo?: string
  defaultBranch?: string
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'added'): void
}>()

const name = ref('')
const path = ref('.')
const runtime = ref<'dockerfile' | 'nixpacks' | 'buildpacks' | 'static'>('dockerfile')
const port = ref(8080)
const detecting = ref(false)
const detectionReason = ref('')
const submitting = ref(false)
const error = ref('')

const canSubmit = computed(() => name.value && /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/.test(name.value))

function required(msg: string) {
  return (v: string) => !!v || msg
}
function nameValid(v: string) {
  if (!v) return true
  return /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/.test(v) || 'lower-case alphanumerics + hyphens'
}

function ownerRepo(url: string): { owner: string; repo: string } | null {
  const m = url.match(/github\.com[/:](.+?)\/(.+?)(?:\.git)?$/)
  if (!m) return null
  return { owner: m[1], repo: m[2] }
}

async function detect() {
  if (!props.installationId || !props.defaultRepo) return
  const or = ownerRepo(props.defaultRepo)
  if (!or) return
  detecting.value = true
  detectionReason.value = ''
  try {
    const res = await axios.post('/api/github/detect-runtime', {
      installationId: props.installationId,
      owner: or.owner,
      repo: or.repo,
      branch: props.defaultBranch || 'main',
      path: path.value === '.' ? '' : path.value,
    })
    if (res.data?.runtime && res.data.runtime !== 'unknown') {
      runtime.value = res.data.runtime
    }
    if (res.data?.port) port.value = res.data.port
    detectionReason.value = res.data?.reason || ''
  } catch (e: any) {
    detectionReason.value = `detection failed: ${e?.response?.data?.message || e?.message}`
  } finally {
    detecting.value = false
  }
}

async function submit() {
  if (!canSubmit.value) return
  submitting.value = true
  error.value = ''
  try {
    await axios.post(`/api/projects/${props.projectName}/services`, {
      name: name.value,
      repo: { path: path.value || '.' },
      runtime: runtime.value,
      port: Number(port.value) || 8080,
    })
    emit('added')
  } catch (e: any) {
    error.value = e?.response?.data?.message || e?.message || 'Failed to add service'
  } finally {
    submitting.value = false
  }
}
</script>
