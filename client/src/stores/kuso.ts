import { defineStore } from 'pinia'

export const useKusoStore = defineStore('kuso', {
    state: () => ({
        kuso: {
            version: "dev",
            operatorVersion: "unknown",
            session: false,
            kubernetesVersion: "",
            isAuthenticated: false,
            socket: null as any,
            nextGenSession: {
                username: "",
                token: "",
            },
            auditEnabled: false,
            adminDisabled: false,
            templatesEnabled: true,
            buildPipeline: false,
            consoleEnabled: false,
            metricsEnabled: false,
            sleepEnabled: false,
        },
        buildPipeline: false,
    }),
    /*
    getters: {
        getKuso() {
            return this.kuso
        },
    },
    actions: {
        setKuso(kuso) {
            this.kuso = kuso
        },
    },
    */
})