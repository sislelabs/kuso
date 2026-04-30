<template>
  <v-app>
    <default-view />
  </v-app>
</template>

<script lang="ts" setup>
  import DefaultView from './View.vue'
</script>

<script lang="ts">

import { useKusoStore } from '../../stores/kuso'
import { useCookies } from "vue3-cookies";
import { useSocketIO } from '../../socket.io';

const { cookies } = useCookies();
const token = cookies.get("kuso.JWT_TOKEN");
//console.log("COOKIE token", token);
const { socket } = useSocketIO(token);

// Write socket to pinia
const kusoStore = useKusoStore();
kusoStore.kuso.socket = socket;

</script>