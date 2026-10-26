import { defineConfig, globalIgnores } from 'eslint/config'
import globals from 'globals'
import js from '@eslint/js'
import pluginVue from 'eslint-plugin-vue'
import prettierRecommended from 'eslint-plugin-prettier/recommended'

export default defineConfig([
  {
    name: 'app/files-to-lint',
    files: ['**/*.{vue,js,mjs,jsx}']
  },

  globalIgnores([
    '**/dist/**',
    '**/dist-ssr/**',
    '**/coverage/**',
    '**/wailsjs/**',
    '**/public/**'
  ]),

  {
    languageOptions: {
      globals: {
        ...globals.browser
      }
    }
  },

  {
    // vite.config.js corre en Node, no en el navegador: necesita process,
    // __dirname y compañía sin que no-undef proteste.
    name: 'app/node-files',
    files: ['vite.config.js'],
    languageOptions: {
      globals: {
        ...globals.node
      }
    }
  },

  js.configs.recommended,
  ...pluginVue.configs['flat/recommended'],

  // Prettier como regla de lint (prettier/prettier en error) más la
  // desactivación de reglas de formato en conflicto: sustituye al antiguo
  // skipFormatting de eslint-config-prettier.
  prettierRecommended
])
