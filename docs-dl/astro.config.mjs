// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

// https://astro.build/config
export default defineConfig({
	site: 'https://infinityxgame.github.io/TelegramDL/',
	base: '/TelegramDL',
	integrations: [
		starlight({
			title: 'TelegramDL',
			locales: {
				root: {
					label: 'Español',
					lang: 'es',
				},
				en: {
					label: 'English',
					lang: 'en',
				},
			},
			social: [
				{ icon: 'github', label: 'GitHub', href: 'https://github.com/infinityxgame/TelegramDL' }
			],
			sidebar: [
				{
					label: 'Inicio',
					translations: {
						en: 'Home',
					},
					link: '/',
				},
				{
					label: 'Descargar',
					translations: {
						en: 'Download',
					},
					link: '/download',
				},
				{
					label: 'Guía de Inicio',
					translations: {
						en: 'Getting Started',
					},
					link: '/getting-started',
				},
				{
					label: 'Conexión y Autenticación',
					translations: {
						en: 'Connection & Authentication',
					},
					link: '/connection-auth',
				},
				{
					label: 'Funcionalidades',
					translations: {
						en: 'Features',
					},
					link: '/features',
				},
				{
					label: 'Modo Escucha',
					translations: {
						en: 'Listener Mode',
					},
					link: '/listener',
				},
				{
					label: 'Escáner Histórico',
					translations: {
						en: 'Historical Scanner',
					},
					link: '/scanner',
				},
				{
					label: 'Ajustes y Configuración',
					translations: {
						en: 'Settings',
					},
					link: '/settings',
				},
				{
					label: 'Notificaciones',
					translations: {
						en: 'Notifications',
					},
					link: '/notifications',
				},
				{
					label: 'Acceso Remoto',
					translations: {
						en: 'Remote Access',
					},
					link: '/remote-access',
				},
				{
					label: 'Referencia de API',
					translations: {
						en: 'API Reference',
					},
					link: '/api-reference',
				},
				{
					label: 'Comandos CLI',
					translations: {
						en: 'CLI Commands',
					},
					link: '/cli',
				},
				{
					label: 'Arquitectura',
					translations: {
						en: 'Architecture',
					},
					link: '/architecture',
				},
			],
			customCss: [
				'./src/styles/custom.css',
			],
			components: {
				Header: './src/components/Header.astro',
				ThemeSelect: './src/components/ThemeSelect.astro',
                PageFrame: './src/components/PageFrame.astro',
			},
		}),
	],
});
