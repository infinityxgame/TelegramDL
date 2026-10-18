// Este script vive fuera de index.html por la CSP del hallazgo SEC-07:
// la directiva script-src 'self' no admite scripts inline, así que el IIFE
// que aplica el tema del usuario (tgdl_user.color_id) antes del primer
// pintado se sirve ahora como recurso del propio origen (/theme-init.js).
//
// DEBE seguir siendo un script clásico y síncrono dentro de <head>, sin
// type="module": los módulos se ejecutan diferidos (tras parsear el
// documento) y el tema parpadearía en blanco antes de aplicarse.
//
// El IIFE de debajo se copió verbatim (línea a línea) de index.html, así que
// queda fuera del formateador y de las reglas de bloques vacíos: reformatar
// sus líneas o «arreglar» el catch vacío con su parámetro sin usar sería
// dejar de ser idéntico al original. eslint.config.mjs está fuera del
// alcance de este hallazgo, así que las reglas se desactivan aquí.
/* eslint-disable prettier/prettier, no-unused-vars, no-empty -- extracción verbatim de index.html (SEC-07) */
      (function() {
        try {
          const user = JSON.parse(localStorage.getItem('tgdl_user') || 'null');
          if (user && user.color_id !== undefined) {
            const themeMap = {
              0: { primary: '#c0514a', accent: '#e67d77', bgBase: '#160808', bgTop: '#3a1614', surface: '#220e0d', surfaceLight: '#2d1211', border: '#4a1e1b', borderLight: '#632824', iconBg: '#3a1614', glow: 'rgba(192, 81, 74, 0.15)', textDim: '#a87e7b', gradient: '#c0514a' },
              1: { primary: '#c87a2f', accent: '#e6a567', bgBase: '#161108', bgTop: '#3a2a14', surface: '#22190d', surfaceLight: '#2d2111', border: '#4a351b', borderLight: '#634724', iconBg: '#3a2a14', glow: 'rgba(200, 122, 47, 0.15)', textDim: '#a8927b', gradient: '#c87a2f' },
              2: { primary: '#8b62cf', accent: '#b69df2', bgBase: '#10081a', bgTop: '#26164d', surface: '#170b2e', surfaceLight: '#1f0e3d', border: '#301b5b', borderLight: '#40237a', iconBg: '#26164d', glow: 'rgba(139, 98, 207, 0.15)', textDim: '#8f7ba8', gradient: '#8b62cf' },
              3: { primary: '#479f29', accent: '#76c859', bgBase: '#081608', bgTop: '#163a14', surface: '#0d220d', surfaceLight: '#112d11', border: '#1b4a1b', borderLight: '#246324', iconBg: '#163a14', glow: 'rgba(71, 159, 41, 0.15)', textDim: '#7ba87b', gradient: '#479f29' },
              4: { primary: '#3fa7b5', accent: '#7cd1db', bgBase: '#081616', bgTop: '#143a3d', surface: '#0d2222', surfaceLight: '#112d2d', border: '#1b4a4d', borderLight: '#246366', iconBg: '#143a3d', glow: 'rgba(63, 167, 181, 0.15)', textDim: '#7ba8a8', gradient: '#3fa7b5' },
              5: { primary: '#38a7ff', accent: '#5ebcff', bgBase: '#07111f', bgTop: '#163557', surface: '#0b1a2a', surfaceLight: '#0e2032', border: '#1b344b', borderLight: '#234765', iconBg: '#11385b', glow: 'rgba(73, 182, 255, 0.15)', textDim: '#728ba2', gradient: '#38a7ff' },
              6: { primary: '#c04c7d', accent: '#e67dac', bgBase: '#160811', bgTop: '#3a1428', surface: '#220d18', surfaceLight: '#2d111f', border: '#4a1b32', borderLight: '#632442', iconBg: '#3a1428', glow: 'rgba(192, 76, 125, 0.15)', textDim: '#a87b92', gradient: '#c04c7d' },
              7: { primary: '#7d8b99', accent: '#acb8c2', bgBase: '#121416', bgTop: '#282d33', surface: '#1a1e22', surfaceLight: '#22282d', border: '#353d45', borderLight: '#45505a', iconBg: '#282d33', glow: 'rgba(125, 139, 153, 0.15)', textDim: '#888b8e', gradient: '#7d8b99' },
              8: { primary: '#c0514a', accent: '#e67d77', bgBase: '#160808', bgTop: '#3a1614', surface: '#220e0d', surfaceLight: '#2d1211', border: '#4a1e1b', borderLight: '#632824', iconBg: '#3a1614', glow: 'rgba(192, 81, 74, 0.15)', textDim: '#a87e7b', gradient: 'linear-gradient(135deg, #c0514a 0%, #f08c5d 100%)' },
              9: { primary: '#c87a2f', accent: '#e6a567', bgBase: '#161108', bgTop: '#3a2a14', surface: '#22190d', surfaceLight: '#2d2111', border: '#4a351b', borderLight: '#634724', iconBg: '#3a2a14', glow: 'rgba(200, 122, 47, 0.15)', textDim: '#a8927b', gradient: 'linear-gradient(135deg, #c87a2f 0%, #f2bc42 100%)' },
              10: { primary: '#8b62cf', accent: '#b69df2', bgBase: '#10081a', bgTop: '#26164d', surface: '#170b2e', surfaceLight: '#1f0e3d', border: '#301b5b', borderLight: '#40237a', iconBg: '#26164d', glow: 'rgba(139, 98, 207, 0.15)', textDim: '#8f7ba8', gradient: 'linear-gradient(135deg, #8b62cf 0%, #d66fd6 100%)' },
              11: { primary: '#479f29', accent: '#76c859', bgBase: '#081608', bgTop: '#163a14', surface: '#0d220d', surfaceLight: '#112d11', border: '#1b4a1b', borderLight: '#246324', iconBg: '#163a14', glow: 'rgba(71, 159, 41, 0.15)', textDim: '#7ba87b', gradient: 'linear-gradient(135deg, #479f29 0%, #a6c450 100%)' },
              12: { primary: '#3fa7b5', accent: '#7cd1db', bgBase: '#081616', bgTop: '#143a3d', surface: '#0d2222', surfaceLight: '#112d2d', border: '#1b4a4d', borderLight: '#246366', iconBg: '#143a3d', glow: 'rgba(63, 167, 181, 0.15)', textDim: '#7ba8a8', gradient: 'linear-gradient(135deg, #3fa7b5 0%, #62d4e3 100%)' },
              13: { primary: '#38a7ff', accent: '#5ebcff', bgBase: '#07111f', bgTop: '#163557', surface: '#0b1a2a', surfaceLight: '#0e2032', border: '#1b344b', borderLight: '#234765', iconBg: '#11385b', glow: 'rgba(73, 182, 255, 0.15)', textDim: '#728ba2', gradient: 'linear-gradient(135deg, #38a7ff 0%, #b48bf2 100%)' },
              14: { primary: '#c04c7d', accent: '#e67dac', bgBase: '#160811', bgTop: '#3a1428', surface: '#220d18', surfaceLight: '#2d111f', border: '#4a1b32', borderLight: '#632442', iconBg: '#3a1428', glow: 'rgba(192, 76, 125, 0.15)', textDim: '#a87b92', gradient: 'linear-gradient(135deg, #c04c7d 0%, #f28b7e 100%)' },
              15: { primary: '#7d8b99', accent: '#acb8c2', bgBase: '#121416', bgTop: '#282d33', surface: '#1a1e22', surfaceLight: '#22282d', border: '#353d45', borderLight: '#45505a', iconBg: '#282d33', glow: 'rgba(125, 139, 153, 0.15)', textDim: '#888888', gradient: 'linear-gradient(135deg, #7d8b99 0%, #b0b8c2 100%)' },
              // Temas especiales del panel (ver themes-dl.css).
              16: { primary: '#d1618f', accent: '#b79af5', bgBase: '#0d0a1e', bgTop: '#3a2568', surface: '#160f2d', surfaceLight: '#1d1539', border: '#2f2258', borderLight: '#41307a', iconBg: '#2f2163', glow: 'rgba(192, 81, 140, 0.20)', textDim: '#9086bb', gradient: 'linear-gradient(135deg, #5b45b0 0%, #c0518c 52%, #f0803c 100%)' },
              17: { primary: '#e0ff00', accent: '#00e5ff', bgBase: '#0a0a0c', bgTop: '#14161d', surface: '#16181e', surfaceLight: '#1e2029', border: '#272a33', borderLight: '#3a3e4a', iconBg: '#1f2230', glow: 'rgba(224, 255, 0, 0.18)', textDim: '#a0a5b5', gradient: 'linear-gradient(120deg, #e0ff00 0%, #a8f53c 42%, #00e5ff 100%)' },
              18: { primary: '#e6ecef', accent: '#8fa4b5', bgBase: '#0f1c11', bgTop: '#2d5a27', surface: '#1a3318', surfaceLight: '#224021', border: '#3a5361', borderLight: '#5c768d', iconBg: '#26402c', glow: 'rgba(230, 236, 239, 0.16)', textDim: '#93a79b', gradient: 'linear-gradient(120deg, #e6ecef 0%, #b9c9d4 46%, #8fa4b5 100%)' },
              19: { primary: '#f48fb1', accent: '#2a6f69', bgBase: '#e3dbea', bgTop: '#f0cfdc', surface: '#f4eef6', surfaceLight: '#ece2ee', border: '#d6c9d7', borderLight: '#bfb0c2', iconBg: '#f0d9e3', glow: 'rgba(244, 143, 177, 0.25)', textDim: '#6b6270', gradient: 'linear-gradient(120deg, #ffb7c5 0%, #f48fb1 52%, #a0e7e5 100%)' },
              20: { primary: '#0a84ff', accent: '#64d2ff', bgBase: '#09090b', bgTop: '#10131a', surface: '#1c1c1e', surfaceLight: '#2c2c2e', border: '#2c2c2e', borderLight: '#3a3a3c', iconBg: '#26262a', glow: 'rgba(10, 132, 255, 0.28)', textDim: '#98989f', gradient: 'linear-gradient(180deg, #3e9bff 0%, #0a84ff 100%)' },
              21: { primary: '#7fb069', accent: '#8fc0d4', bgBase: '#141f18', bgTop: '#25402f', surface: '#1a261d', surfaceLight: '#223026', border: '#2c4550', borderLight: '#3d6272', iconBg: '#22392b', glow: 'rgba(127, 176, 105, 0.20)', textDim: '#8fa596', gradient: 'linear-gradient(120deg, #4a7c59 0%, #7fb069 58%, #99c2a2 100%)' },
              22: { primary: '#d32f2f', accent: '#00e5ff', bgBase: '#14050c', bgTop: '#3c0a18', surface: '#481019', surfaceLight: '#5a1622', border: '#6e2230', borderLight: '#8a3546', iconBg: '#330b13', glow: 'rgba(211, 47, 47, 0.22)', textDim: '#e3b3bd', gradient: 'linear-gradient(120deg, #c62828 0%, #d32f2f 48%, #f48fb1 100%)' },
              23: { primary: '#007aff', accent: '#007aff', bgBase: '#000000', bgTop: '#121212', surface: '#121212', surfaceLight: '#1c1c1e', border: '#2c2c2e', borderLight: '#3a3a3c', iconBg: '#1c1c1e', glow: 'rgba(0, 122, 255, 0.25)', textDim: '#8e8e93', gradient: 'linear-gradient(135deg, #007aff 0%, #0a84ff 50%, #5ac8fa 100%)' }
            };
            const theme = themeMap[user.color_id] || themeMap[5];
            const root = document.documentElement;
            root.style.setProperty('--user-primary', theme.primary);
            root.style.setProperty('--user-accent', theme.accent);
            root.style.setProperty('--user-bg-base', theme.bgBase);
            root.style.setProperty('--user-bg-top', theme.bgTop);
            root.style.setProperty('--user-surface', theme.surface);
            root.style.setProperty('--user-surface-light', theme.surfaceLight);
            root.style.setProperty('--user-border', theme.border);
            root.style.setProperty('--user-border-light', theme.borderLight);
            root.style.setProperty('--user-icon-bg', theme.iconBg);
            root.style.setProperty('--user-glow', theme.glow);
            root.style.setProperty('--user-text-dim', theme.textDim);
            root.style.setProperty('--user-gradient', theme.gradient);
            var specialThemeClasses = { 16: 'theme-twilight-comet', 17: 'theme-edge-runner', 18: 'theme-rainy-garden', 19: 'theme-silent-cherry', 20: 'theme-osx', 21: 'theme-herbal-apothecary', 22: 'theme-darling-red', 23: 'theme-udyat' };
            var lightThemeIds = [19];
            var themeId = themeMap[user.color_id] ? user.color_id : 5;
            var themeClass = specialThemeClasses[themeId];
            if (themeClass) root.classList.add(themeClass);
            if (lightThemeIds.indexOf(Number(themeId)) !== -1) root.classList.add('theme-light');
          }
        } catch (e) {}
      })();
