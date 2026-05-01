<template>
  <v-container fluid>
    <v-row class="mb-4">
      <v-col>
        <div class="text-h4">Projects</div>
        <div class="text-body-2 text-medium-emphasis">
          One product per project. Connect a repo, kuso deploys it.
        </div>
      </v-col>
      <v-col cols="auto" class="d-flex align-center">
        <v-btn
          color="primary"
          prepend-icon="mdi-plus"
          @click="$router.push('/projects/new')"
        >
          New Project
        </v-btn>
      </v-col>
    </v-row>

    <v-progress-linear v-if="loading" indeterminate />

    <v-row v-if="!loading && projects.length === 0">
      <v-col>
        <v-card variant="outlined" class="pa-12 text-center">
          <v-icon size="64" color="primary" class="mb-4">mdi-rocket-launch-outline</v-icon>
          <div class="text-h6 mb-2">No projects yet</div>
          <div class="text-body-2 text-medium-emphasis mb-6">
            Connect a GitHub repo to deploy your first service.
          </div>
          <v-btn color="primary" @click="$router.push('/projects/new')">
            Create your first project
          </v-btn>
        </v-card>
      </v-col>
    </v-row>

    <v-row v-else>
      <v-col v-for="p in projects" :key="p.metadata.name" cols="12" md="6" lg="4">
        <v-card
          variant="outlined"
          class="project-card"
          @click="$router.push(`/projects/${p.metadata.name}`)"
          link
        >
          <v-card-item>
            <v-card-title>{{ p.metadata.name }}</v-card-title>
            <v-card-subtitle v-if="p.spec.description">
              {{ p.spec.description }}
            </v-card-subtitle>
          </v-card-item>
          <v-card-text>
            <div class="d-flex align-center mb-2">
              <v-icon size="small" class="mr-2">mdi-source-branch</v-icon>
              <span class="text-caption">{{ shortRepo(p.spec.defaultRepo?.url) }}</span>
            </div>
            <div class="d-flex align-center">
              <v-chip
                v-if="p.spec.previews?.enabled"
                size="x-small"
                color="success"
                class="mr-2"
              >
                Previews on
              </v-chip>
              <v-chip
                v-else
                size="x-small"
                variant="outlined"
                class="mr-2"
              >
                Previews off
              </v-chip>
            </div>
          </v-card-text>
        </v-card>
      </v-col>
    </v-row>
  </v-container>
</template>

<script lang="ts" setup>
import { onMounted, ref } from 'vue'
import axios from 'axios'

interface Project {
  metadata: { name: string }
  spec: {
    description?: string
    defaultRepo?: { url: string; defaultBranch?: string }
    previews?: { enabled?: boolean }
  }
}

const projects = ref<Project[]>([])
const loading = ref(true)

async function load() {
  loading.value = true
  try {
    const res = await axios.get('/api/projects')
    // The API returns either a CR list (with .items) or a flat array;
    // accept both shapes for forward-compat.
    projects.value = Array.isArray(res.data) ? res.data : res.data?.items || []
  } catch (e) {
    // 404 from kubernetes when CRD doesn't exist yet -> show empty state
    projects.value = []
  } finally {
    loading.value = false
  }
}

function shortRepo(url?: string): string {
  if (!url) return '(no repo)'
  const m = url.match(/github\.com[/:](.+?)(?:\.git)?$/)
  return m?.[1] || url
}

onMounted(load)
</script>

<style scoped>
.project-card {
  cursor: pointer;
  transition: border-color 120ms ease;
}
.project-card:hover {
  border-color: rgb(var(--v-theme-primary));
}
</style>
