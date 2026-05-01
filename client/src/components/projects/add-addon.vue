<template>
  <v-card>
    <v-card-title>Add an addon</v-card-title>
    <v-card-text>
      <v-form @submit.prevent="submit">
        <v-text-field
          v-model="name"
          label="Name"
          hint="Used in the connection secret name (project-name-conn)"
          :rules="[required('Name is required')]"
          density="comfortable"
        />
        <v-select
          v-model="kind"
          :items="kinds"
          label="Kind"
          density="comfortable"
          @update:model-value="onKindChange"
        />
        <v-text-field
          v-model="version"
          label="Version"
          hint="Defaults are kind-specific"
          density="comfortable"
        />
        <v-select
          v-model="size"
          :items="['small', 'medium', 'large']"
          label="Size"
          density="comfortable"
        />
        <v-switch
          v-model="ha"
          label="High availability"
          messages="Currently honoured for redis only; other kinds ignore"
          color="primary"
          density="comfortable"
        />
        <v-alert v-if="!implemented" type="warning" variant="tonal" class="mt-2">
          <strong>{{ kind }}</strong> support is reserved for v0.2.x — adding it now creates a
          marker resource but no actual workload.
        </v-alert>
        <v-alert v-if="error" type="error" variant="tonal" class="mt-2">{{ error }}</v-alert>
      </v-form>
    </v-card-text>
    <v-card-actions>
      <v-spacer />
      <v-btn variant="text" @click="$emit('close')">Cancel</v-btn>
      <v-btn color="primary" :loading="submitting" :disabled="!canSubmit" @click="submit">
        Add addon
      </v-btn>
    </v-card-actions>
  </v-card>
</template>

<script lang="ts" setup>
import { computed, ref } from 'vue'
import axios from 'axios'

const props = defineProps<{ projectName: string }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'added'): void }>()

const kinds = [
  'postgres',
  'redis',
  'mongodb',
  'mysql',
  'rabbitmq',
  'memcached',
  'clickhouse',
  'elasticsearch',
  'kafka',
  'cockroachdb',
  'couchdb',
]
const fullySupportedKinds = new Set(['postgres', 'redis'])

const name = ref('')
const kind = ref<typeof kinds[number]>('postgres')
const version = ref('16')
const size = ref<'small' | 'medium' | 'large'>('small')
const ha = ref(false)
const submitting = ref(false)
const error = ref('')

const implemented = computed(() => fullySupportedKinds.has(kind.value))
const canSubmit = computed(() => !!name.value)

function required(msg: string) {
  return (v: string) => !!v || msg
}

function onKindChange(k: string) {
  // Sane default versions per kind
  const defaults: Record<string, string> = {
    postgres: '16',
    redis: '7',
    mongodb: '7',
    mysql: '8',
    rabbitmq: '3',
    memcached: '1.6',
    clickhouse: '24',
    elasticsearch: '8',
    kafka: '3',
    cockroachdb: '23',
    couchdb: '3',
  }
  version.value = defaults[k] || ''
}

async function submit() {
  if (!canSubmit.value) return
  submitting.value = true
  error.value = ''
  try {
    await axios.post(`/api/projects/${props.projectName}/addons`, {
      name: name.value,
      kind: kind.value,
      version: version.value || undefined,
      size: size.value,
      ha: ha.value,
    })
    emit('added')
  } catch (e: any) {
    error.value = e?.response?.data?.message || e?.message || 'Failed to add addon'
  } finally {
    submitting.value = false
  }
}
</script>
