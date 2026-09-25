// Sistema de temas del panel (extraído de App.vue).
//
// Módulo de utilidades sin estado reactivo: expone themeMap y las funciones
// applyTheme / applyLoaderTheme / loadPublicTheme / initTheme con el mismo
// comportamiento que tenían inline. No es un composable porque no maneja
// estado de instancia de componente (ver CONTRIBUTING.md): si algún día
// posee un `activeThemeId` reactivo con sus observadores, entonces sí deberá
// convertirse en `composables/useTheme.js`.

export const themeMap = {
  // --- Fila Superior: Sólidos ---
  0: { primary: '#c0514a', secondary: '#c0514a', accent: '#e67d77', bgBase: '#160808', bgTop: '#3a1614', surface: '#220e0d', surfaceLight: '#2d1211', border: '#4a1e1b', borderLight: '#632824', iconBg: '#3a1614', glow: 'rgba(192, 81, 74, 0.15)', textDim: '#a87e7b', gradient: '#c0514a' },
  1: { primary: '#c87a2f', secondary: '#c87a2f', accent: '#e6a567', bgBase: '#161108', bgTop: '#3a2a14', surface: '#22190d', surfaceLight: '#2d2111', border: '#4a351b', borderLight: '#634724', iconBg: '#3a2a14', glow: 'rgba(200, 122, 47, 0.15)', textDim: '#a8927b', gradient: '#c87a2f' },
  2: { primary: '#8b62cf', secondary: '#8b62cf', accent: '#b69df2', bgBase: '#10081a', bgTop: '#26164d', surface: '#170b2e', surfaceLight: '#1f0e3d', border: '#301b5b', borderLight: '#40237a', iconBg: '#26164d', glow: 'rgba(139, 98, 207, 0.15)', textDim: '#8f7ba8', gradient: '#8b62cf' },
  3: { primary: '#479f29', secondary: '#479f29', accent: '#76c859', bgBase: '#081608', bgTop: '#163a14', surface: '#0d220d', surfaceLight: '#112d11', border: '#1b4a1b', borderLight: '#246324', iconBg: '#163a14', glow: 'rgba(71, 159, 41, 0.15)', textDim: '#7ba87b', gradient: '#479f29' },
  4: { primary: '#3fa7b5', secondary: '#3fa7b5', accent: '#7cd1db', bgBase: '#081616', bgTop: '#143a3d', surface: '#0d2222', surfaceLight: '#112d2d', border: '#1b4a4d', borderLight: '#246366', iconBg: '#143a3d', glow: 'rgba(63, 167, 181, 0.15)', textDim: '#7ba8a8', gradient: '#3fa7b5' },
  5: { primary: '#38a7ff', secondary: '#38a7ff', accent: '#5ebcff', bgBase: '#07111f', bgTop: '#163557', surface: '#0b1a2a', surfaceLight: '#0e2032', border: '#1b344b', borderLight: '#234765', iconBg: '#11385b', glow: 'rgba(73, 182, 255, 0.15)', textDim: '#728ba2', gradient: '#38a7ff' },
  6: { primary: '#c04c7d', secondary: '#c04c7d', accent: '#e67dac', bgBase: '#160811', bgTop: '#3a1428', surface: '#220d18', surfaceLight: '#2d111f', border: '#4a1b32', borderLight: '#632442', iconBg: '#3a1428', glow: 'rgba(192, 76, 125, 0.15)', textDim: '#a87b92', gradient: '#c04c7d' },
  7: { primary: '#7d8b99', secondary: '#7d8b99', accent: '#acb8c2', bgBase: '#121416', bgTop: '#282d33', surface: '#1a1e22', surfaceLight: '#22282d', border: '#353d45', borderLight: '#45505a', iconBg: '#282d33', glow: 'rgba(125, 139, 153, 0.15)', textDim: '#888b8e', gradient: '#7d8b99' },

  // --- Fila Inferior: Degradados ---
  8: { primary: '#c0514a', secondary: '#f08c5d', accent: '#e67d77', bgBase: '#160808', bgTop: '#3a1614', surface: '#220e0d', surfaceLight: '#2d1211', border: '#4a1e1b', borderLight: '#632824', iconBg: '#3a1614', glow: 'rgba(192, 81, 74, 0.15)', textDim: '#a87e7b', gradient: 'linear-gradient(135deg, #c0514a 0%, #f08c5d 100%)' },
  9: { primary: '#c87a2f', secondary: '#f2bc42', accent: '#e6a567', bgBase: '#161108', bgTop: '#3a2a14', surface: '#22190d', surfaceLight: '#2d2111', border: '#4a351b', borderLight: '#634724', iconBg: '#3a2a14', glow: 'rgba(200, 122, 47, 0.15)', textDim: '#a8927b', gradient: 'linear-gradient(135deg, #c87a2f 0%, #f2bc42 100%)' },
  10: { primary: '#8b62cf', secondary: '#d66fd6', accent: '#b69df2', bgBase: '#10081a', bgTop: '#26164d', surface: '#170b2e', surfaceLight: '#1f0e3d', border: '#301b5b', borderLight: '#40237a', iconBg: '#26164d', glow: 'rgba(139, 98, 207, 0.15)', textDim: '#8f7ba8', gradient: 'linear-gradient(135deg, #8b62cf 0%, #d66fd6 100%)' },
  11: { primary: '#479f29', secondary: '#a6c450', accent: '#76c859', bgBase: '#081608', bgTop: '#163a14', surface: '#0d220d', surfaceLight: '#112d11', border: '#1b4a1b', borderLight: '#246324', iconBg: '#163a14', glow: 'rgba(71, 159, 41, 0.15)', textDim: '#7ba87b', gradient: 'linear-gradient(135deg, #479f29 0%, #a6c450 100%)' },
  12: { primary: '#3fa7b5', secondary: '#62d4e3', accent: '#7cd1db', bgBase: '#081616', bgTop: '#143a3d', surface: '#0d2222', surfaceLight: '#112d2d', border: '#1b4a4d', borderLight: '#246366', iconBg: '#143a3d', glow: 'rgba(63, 167, 181, 0.15)', textDim: '#7ba8a8', gradient: 'linear-gradient(135deg, #3fa7b5 0%, #62d4e3 100%)' },
  13: { primary: '#38a7ff', secondary: '#b48bf2', accent: '#5ebcff', bgBase: '#07111f', bgTop: '#163557', surface: '#0b1a2a', surfaceLight: '#0e2032', border: '#1b344b', borderLight: '#234765', iconBg: '#11385b', glow: 'rgba(73, 182, 255, 0.15)', textDim: '#728ba2', gradient: 'linear-gradient(135deg, #38a7ff 0%, #b48bf2 100%)' },
  14: { primary: '#c04c7d', secondary: '#f28b7e', accent: '#e67dac', bgBase: '#160811', bgTop: '#3a1428', surface: '#220d18', surfaceLight: '#2d111f', border: '#4a1b32', borderLight: '#632442', iconBg: '#3a1428', glow: 'rgba(192, 76, 125, 0.15)', textDim: '#a87b92', gradient: 'linear-gradient(135deg, #c04c7d 0%, #f28b7e 100%)' },
  15: { primary: '#7d8b99', secondary: '#b0b8c2', accent: '#acb8c2', bgBase: '#121416', bgTop: '#282d33', surface: '#1a1e22', surfaceLight: '#22282d', border: '#353d45', borderLight: '#45505a', iconBg: '#282d33', glow: 'rgba(125, 139, 153, 0.15)', textDim: '#888888', gradient: 'linear-gradient(135deg, #7d8b99 0%, #b0b8c2 100%)' },

  // --- Temas especiales del panel ---
  // Del 16 en adelante ya no son colores de Telegram Premium sino paletas
  // propias, y además de las variables de color encienden una capa decorativa
  // (ver themes-dl.css) a través de la clase que pone applyTheme en el
  // <html>. El backend guarda el id tal cual, sin validar rango, así que
  // añadir temas aquí no necesita ningún cambio en Go.
  16: { name: 'Twilight Comet', especial: true, primary: '#d1618f', secondary: '#f5b96b', accent: '#b79af5', bgBase: '#0d0a1e', bgTop: '#3a2568', surface: '#160f2d', surfaceLight: '#1d1539', border: '#2f2258', borderLight: '#41307a', iconBg: '#2f2163', glow: 'rgba(192, 81, 140, 0.20)', textDim: '#9086bb', gradient: 'linear-gradient(135deg, #5b45b0 0%, #c0518c 52%, #f0803c 100%)' },
  17: { name: 'Edge Runner UI', especial: true, secondary: '#00e5ff', primary: '#e0ff00', accent: '#00e5ff', bgBase: '#0a0a0c', bgTop: '#14161d', surface: '#16181e', surfaceLight: '#1e2029', border: '#272a33', borderLight: '#3a3e4a', iconBg: '#1f2230', glow: 'rgba(224, 255, 0, 0.18)', textDim: '#a0a5b5', gradient: 'linear-gradient(120deg, #e0ff00 0%, #a8f53c 42%, #00e5ff 100%)' },
  18: { name: 'Rainy Garden', especial: true, secondary: '#8fa4b5', primary: '#e6ecef', accent: '#8fa4b5', bgBase: '#0f1c11', bgTop: '#2d5a27', surface: '#1a3318', surfaceLight: '#224021', border: '#3a5361', borderLight: '#5c768d', iconBg: '#26402c', glow: 'rgba(230, 236, 239, 0.16)', textDim: '#93a79b', gradient: 'linear-gradient(120deg, #e6ecef 0%, #b9c9d4 46%, #8fa4b5 100%)' },
  19: { name: 'Silent Cherry', especial: true, secondary: '#a0e7e5', primary: '#f48fb1', accent: '#2a6f69', bgBase: '#e3dbea', bgTop: '#f0cfdc', surface: '#f4eef6', surfaceLight: '#ece2ee', border: '#d6c9d7', borderLight: '#bfb0c2', iconBg: '#f0d9e3', glow: 'rgba(244, 143, 177, 0.25)', textDim: '#6b6270', gradient: 'linear-gradient(120deg, #ffb7c5 0%, #f48fb1 52%, #a0e7e5 100%)' },
  20: { name: 'osX', especial: true, secondary: '#64d2ff', primary: '#0a84ff', accent: '#64d2ff', bgBase: '#09090b', bgTop: '#10131a', surface: '#1c1c1e', surfaceLight: '#2c2c2e', border: '#2c2c2e', borderLight: '#3a3a3c', iconBg: '#26262a', glow: 'rgba(10, 132, 255, 0.28)', textDim: '#98989f', gradient: 'linear-gradient(180deg, #3e9bff 0%, #0a84ff 100%)' },
  21: { name: 'Herbal Apothecary', especial: true, secondary: '#2b586c', primary: '#7fb069', accent: '#8fc0d4', bgBase: '#141f18', bgTop: '#25402f', surface: '#1a261d', surfaceLight: '#223026', border: '#2c4550', borderLight: '#3d6272', iconBg: '#22392b', glow: 'rgba(127, 176, 105, 0.20)', textDim: '#8fa596', gradient: 'linear-gradient(120deg, #4a7c59 0%, #7fb069 58%, #99c2a2 100%)' },
  22: { name: 'Darling Red', especial: true, secondary: '#f48fb1', primary: '#d32f2f', accent: '#00e5ff', bgBase: '#14050c', bgTop: '#3c0a18', surface: '#481019', surfaceLight: '#5a1622', border: '#6e2230', borderLight: '#8a3546', iconBg: '#330b13', glow: 'rgba(211, 47, 47, 0.22)', textDim: '#e3b3bd', gradient: 'linear-gradient(120deg, #c62828 0%, #d32f2f 48%, #f48fb1 100%)' }
}

// Clase que enciende el decorado de cada tema especial. Los colores de Telegram
// (0-15) no tienen entrada aquí y no pintan nada extra: solo cambian variables.
const clasesTemaEspecial = {
  16: 'tema-twilight-comet',
  17: 'tema-edge-runner',
  18: 'tema-rainy-garden',
  19: 'tema-silent-cherry',
  20: 'tema-osx',
  21: 'tema-herbal-apothecary',
  22: 'tema-darling-red'
}

// Los temas de fondo claro llevan además la clase `tema-claro`, que enciende
// un bloque común de themes-dl.css. El panel tiene más de cien colores
// de texto fijos escritos para fondo oscuro y hay que pisarlos una sola vez,
// no una por cada tema claro que se añada.
const temasClaros = new Set([19])

const aplicarClaseTema = (colorId) => {
  const root = document.documentElement
  Object.values(clasesTemaEspecial).forEach(clase => root.classList.remove(clase))
  root.classList.remove('tema-claro')
  const clase = clasesTemaEspecial[colorId]
  if (clase) root.classList.add(clase)
  // Number(): el id puede llegar como texto desde el almacenamiento del
  // navegador, y un Set no hace la conversión por su cuenta.
  if (temasClaros.has(Number(colorId))) root.classList.add('tema-claro')
}

export const applyLoaderTheme = (colorId) => {
  const theme = themeMap[colorId] || themeMap[5]
  const root = document.documentElement
  root.style.setProperty('--loader-primary', theme.primary)
  root.style.setProperty('--loader-glow', theme.glow)
  localStorage.setItem('tgdl_loader_color', colorId)
}

export const applyTheme = (colorId) => {
  const theme = themeMap[colorId] || themeMap[5]
  const root = document.documentElement
  root.style.setProperty('--user-primary', theme.primary)
  root.style.setProperty('--user-accent', theme.accent)
  root.style.setProperty('--user-bg-base', theme.bgBase)
  root.style.setProperty('--user-bg-top', theme.bgTop)
  root.style.setProperty('--user-surface', theme.surface)
  root.style.setProperty('--user-surface-light', theme.surfaceLight)
  root.style.setProperty('--user-border', theme.border)
  root.style.setProperty('--user-border-light', theme.borderLight)
  root.style.setProperty('--user-icon-bg', theme.iconBg)
  root.style.setProperty('--user-glow', theme.glow)
  root.style.setProperty('--user-text-dim', theme.textDim)
  root.style.setProperty('--user-gradient', theme.gradient)
  aplicarClaseTema(themeMap[colorId] ? colorId : 5)

  // Persistir el color en el objeto de usuario para que index.html lo use en el arranque
  try {
    const user = JSON.parse(localStorage.getItem('tgdl_user') || 'null')
    if (user) {
      user.color_id = colorId
      localStorage.setItem('tgdl_user', JSON.stringify(user))
    }
  } catch (e) { console.error(e) }
}

// En un dispositivo nuevo no hay nada guardado, así que la pantalla de acceso
// remoto saldría con el azul por defecto. El color del panel se puede consultar
// sin token (es solo un número, no dice nada de la cuenta), así que lo pedimos
// para que el login ya se vea con el color que el usuario tiene configurado.
export const loadPublicTheme = async () => {
  try {
    const response = await fetch('/api/theme')
    if (!response.ok) return
    const data = await response.json()
    if (data.color_id === undefined || data.color_id === null) return
    applyTheme(data.color_id)
    applyLoaderTheme(
      data.loader_color_id === undefined || data.loader_color_id === null
        ? data.color_id
        : data.loader_color_id
    )
  } catch {
    // Sin respuesta del servidor se queda el color por defecto.
  }
}

// Aplica el tema guardado en localStorage. Antes corría como efecto lateral en
// la importación de App.vue; ahora es explícito para que importar el módulo
// no pinte el DOM y sea comprobable.
export const initTheme = () => {
  const storedUser = JSON.parse(localStorage.getItem('tgdl_user') || 'null')
  const storedLoaderColor = localStorage.getItem('tgdl_loader_color')

  if (storedUser && storedUser.color_id !== undefined) {
    applyTheme(storedUser.color_id)
    if (storedLoaderColor !== null) {
      applyLoaderTheme(parseInt(storedLoaderColor))
    } else {
      applyLoaderTheme(storedUser.loader_color_id !== undefined ? storedUser.loader_color_id : storedUser.color_id)
    }
  } else {
    applyTheme(5)
    applyLoaderTheme(storedLoaderColor !== null ? parseInt(storedLoaderColor) : 5)
  }
}
