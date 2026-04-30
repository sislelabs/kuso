<template>
  <v-footer padless>
    <v-col
      cols="12"
      class="grey lighten-2 text-center"
    >
      <strong>Kuso</strong> {{ kusoversion }} | 
      <strong>Kubernetes API</strong> {{ kubeversion }}
    </v-col>
  </v-footer>
</template>
<script lang="ts">
export default {

  props: [
    'kubeversion',
    'kusoversion',
  ],
}
</script>