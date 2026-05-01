// Composables
import { createRouter, createWebHistory } from 'vue-router'

// v0.2 redesign — see docs/REDESIGN.md.
//
// "/" is the projects list. Pipelines/apps/phases are gone.
// Templates, Accounts, Notifications, Pod Sizes, Runpacks, Activity stay
// available as legacy admin pages but are not surfaced on the main nav.

const routes = [
  {
    path: '/',
    component: () => import('@/layouts/default/Default.vue'),
    children: [
      {
        path: '/',
        name: 'Projects',
        component: () => import('@/views/Projects.vue'),
      },
      {
        path: '/projects/new',
        name: 'New Project',
        component: () => import('@/views/ProjectNew.vue'),
      },
      {
        path: '/projects/:project',
        name: 'Project',
        props: true,
        component: () => import('@/views/ProjectDetail.vue'),
      },
    ],
  },
  {
    path: '/profile',
    component: () => import('@/layouts/default/Default.vue'),
    children: [
      {
        path: '/profile',
        name: 'Profile',
        component: () => import('@/views/Profile.vue'),
      },
    ],
  },
  {
    path: '/templates',
    component: () => import('@/layouts/default/Default.vue'),
    children: [
      {
        path: '/templates',
        name: 'Templates',
        component: () => import('@/views/Templates.vue'),
      },
    ],
  },
  {
    path: '/accounts',
    component: () => import('@/layouts/default/Default.vue'),
    meta: { requiresUserWrite: true },
    children: [
      {
        path: '/accounts',
        name: 'Accounts',
        component: () => import('@/views/Accounts.vue'),
      },
    ],
  },
  {
    path: '/login',
    component: () => import('@/layouts/login/Login.vue'),
    children: [
      {
        path: '/login',
        name: 'Login',
        component: () => import('@/views/Login.vue'),
      },
    ],
  },
  {
    path: '/setup',
    component: () => import('@/layouts/setup/Setup.vue'),
    children: [
      {
        path: '/setup',
        name: 'Setup',
        component: () => import('@/views/Setup.vue'),
      },
    ],
  },
]

const router = createRouter({
  history: createWebHistory(process.env.BASE_URL),
  routes,
})

export default router
