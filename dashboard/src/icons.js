// Puente entre el panel y el juego de iconos (HugeIcons, versión gratuita, MIT).
//
// Cada icono se exporta con el MISMO nombre que tenía en lucide, así que las
// plantillas no cambian: en cada vista solo cambia la línea de import. Para
// volver a lucide basta con apuntar esos imports de nuevo a 'lucide-vue-next'
// y borrar este archivo; no hay nada más que deshacer.
//
// Para cambiar un icono suelto solo hay que tocar su línea de abajo; el
// catálogo completo está en https://hugeicons.com/icons
import { h } from 'vue'
import { HugeiconsIcon } from '@hugeicons/vue'
import {
  Activity03Icon,
  AlertCircleIcon,
  Download04Icon,
  ArrowLeft02Icon,
  ArrowRight02Icon,
  ArrowUpRight01Icon,
  Tick02Icon,
  CheckmarkCircle02Icon,
  ArrowRight01Icon,
  Clock01Icon,
  Copy01Icon,
  Download01Icon,
  LinkSquare02Icon,
  ViewIcon,
  ViewOffIcon,
  FileValidationIcon,
  FileDownloadIcon,
  File02Icon,
  Folder01Icon,
  DashboardSpeed02Icon,
  HardDriveIcon,
  HelpCircleIcon,
  Image02Icon,
  InboxIcon,
  InformationCircleIcon,
  Key02Icon,
  Loading03Icon,
  Logout03Icon,
  Menu01Icon,
  Message02Icon,
  MusicNote01Icon,
  Notification01Icon,
  PauseIcon,
  PowerIcon,
  SmartPhone01Icon,
  PlayIcon,
  PlusSignIcon,
  Pulse01Icon,
  RadioTowerIcon,
  Refresh01Icon,
  Robot01Icon,
  ArrowReloadHorizontalIcon,
  FloppyDiskIcon,
  Search01Icon,
  Settings02Icon,
  ShieldCheckIcon,
  StarIcon,
  Delete02Icon,
  Upload01Icon,
  UserCheck01Icon,
  Video01Icon,
  Cancel01Icon,
  DeliverySent02Icon,
  FlashIcon,
  FilterIcon,
  LanguagesIcon
} from '@hugeicons/core-free-icons'

// Grosor de línea del juego. HugeIcons no fija ninguno por defecto y sus trazos
// quedan bastante finos; 1.8 se acerca al peso que tenía el panel con lucide.
const STROKE_WIDTH = 1.8

// Envuelve un icono para que se use igual que los de lucide:
// <Download :size="16" class="lo-que-sea" />. Con inheritAttrs desactivado los
// atributos se pasan una sola vez y acaban en el <svg>.
const icon = (glyph) => ({
  inheritAttrs: false,
  setup(_, { attrs }) {
    return () =>
      h(HugeiconsIcon, { icon: glyph, strokeWidth: STROKE_WIDTH, ...attrs })
  }
})

export const Activity = icon(Activity03Icon) // panel de descargas activas
export const AlertCircle = icon(AlertCircleIcon) // avisos y modales de peligro
export const ArrowDownToLine = icon(Download04Icon) // entrada "Descargas" del menú
export const ArrowLeft = icon(ArrowLeft02Icon) // volver en el explorador de carpetas
export const ArrowRight = icon(ArrowRight02Icon) // continuar en el asistente de acceso
export const ArrowUpRight = icon(ArrowUpRight01Icon) // abrir algo fuera de la app
export const Bell = icon(Notification01Icon) // notificación sonora al terminar descargas
export const Bot = icon(Robot01Icon) // avisos que envía el bot de Telegram
export const Check = icon(Tick02Icon) // confirmar la carpeta elegida
export const CheckCircle2 = icon(CheckmarkCircle02Icon) // aviso de operación correcta
export const ChevronRight = icon(ArrowRight01Icon) // entrar en una carpeta o chat
export const Clock3 = icon(Clock01Icon) // descarga en cola
export const Copy = icon(Copy01Icon) // copiar token y registro
export const Download = icon(Download01Icon) // descargar un archivo
export const ExternalLink = icon(LinkSquare02Icon) // abrir el archivo descargado
export const Eye = icon(ViewIcon) // mostrar el token
export const EyeOff = icon(ViewOffIcon) // ocultar el token
export const FileCheck = icon(FileValidationIcon) // descargas completadas
export const FileDown = icon(FileDownloadIcon) // descargas en curso
export const FileText = icon(File02Icon) // documento genérico
export const Filter = icon(FilterIcon) // filtro de archivos
export const Folder = icon(Folder01Icon) // carpeta de descargas
export const Gauge = icon(DashboardSpeed02Icon) // velocidad
export const HardDrive = icon(HardDriveIcon) // unidades de disco
export const HelpCircle = icon(HelpCircleIcon) // modal de confirmación
export const Image = icon(Image02Icon) // foto
export const Inbox = icon(InboxIcon) // bandeja de la escucha
export const Info = icon(InformationCircleIcon) // nota informativa
export const KeyRound = icon(Key02Icon) // credenciales y token
export const Languages = icon(LanguagesIcon) // selector de idioma en ajustes
export const Loader2 = icon(Loading03Icon) // espera en curso
export const LogOut = icon(Logout03Icon) // cerrar sesión de Telegram
export const Menu = icon(Menu01Icon) // menú en pantalla pequeña
export const MessageCircle = icon(Message02Icon) // chat vigilado
export const Music = icon(MusicNote01Icon) // audio
export const Pause = icon(PauseIcon) // pausar
export const Phone = icon(SmartPhone01Icon) // número de teléfono
export const Play = icon(PlayIcon) // reanudar
export const Plus = icon(PlusSignIcon) // añadir chat
export const Power = icon(PowerIcon) // apagar el equipo al terminar la cola
export const Radio = icon(RadioTowerIcon) // entrada "Escucha" del menú
export const RefreshCw = icon(Refresh01Icon) // regenerar o recargar
export const RotateCcw = icon(ArrowReloadHorizontalIcon) // reintentar una descarga
export const Save = icon(FloppyDiskIcon) // guardar ajustes
export const ScrollText = icon(Pulse01Icon) // entrada "Logs" del menú
export const Search = icon(Search01Icon) // buscar dentro del registro
export const Send = icon(DeliverySent02Icon) // enviar el token a Telegram
export const Settings2 = icon(Settings02Icon) // entrada "Ajustes" del menú
export const ShieldCheck = icon(ShieldCheckIcon) // verificación en dos pasos
export const Star = icon(StarIcon) // marcar favorito
export const Trash2 = icon(Delete02Icon) // eliminar
export const Upload = icon(Upload01Icon) // subida
export const UserCheck = icon(UserCheck01Icon) // usuario con sesión iniciada
export const Video = icon(Video01Icon) // vídeo
export const X = icon(Cancel01Icon) // cerrar
export const Zap = icon(FlashIcon) // actualización disponible
