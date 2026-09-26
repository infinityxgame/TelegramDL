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
  PauseIcon,
  PowerIcon,
  SmartPhone01Icon,
  PlayIcon,
  PlusSignIcon,
  Pulse01Icon,
  RadioTowerIcon,
  Refresh01Icon,
  ArrowReloadHorizontalIcon,
  FloppyDiskIcon,
  Search01Icon,
  Settings02Icon,
  ShieldCheckIcon,
  Delete02Icon,
  Upload01Icon,
  UserCheck01Icon,
  Video01Icon,
  Cancel01Icon,
  DeliverySent02Icon,
  FlashIcon
} from '@hugeicons/core-free-icons'

// Grosor de línea del juego. HugeIcons no fija ninguno por defecto y sus trazos
// quedan bastante finos; 1.8 se acerca al peso que tenía el panel con lucide.
const GROSOR = 1.8

// Envuelve un icono para que se use igual que los de lucide:
// <Download :size="16" class="lo-que-sea" />. Con inheritAttrs desactivado los
// atributos se pasan una sola vez y acaban en el <svg>.
const icono = (glifo) => ({
  inheritAttrs: false,
  setup(_, { attrs }) {
    return () => h(HugeiconsIcon, { icon: glifo, strokeWidth: GROSOR, ...attrs })
  }
})

export const Activity = icono(Activity03Icon) // panel de descargas activas
export const AlertCircle = icono(AlertCircleIcon) // avisos y modales de peligro
export const ArrowDownToLine = icono(Download04Icon) // entrada "Descargas" del menú
export const ArrowLeft = icono(ArrowLeft02Icon) // volver en el explorador de carpetas
export const ArrowRight = icono(ArrowRight02Icon) // continuar en el asistente de acceso
export const ArrowUpRight = icono(ArrowUpRight01Icon) // abrir algo fuera de la app
export const Check = icono(Tick02Icon) // confirmar la carpeta elegida
export const CheckCircle2 = icono(CheckmarkCircle02Icon) // aviso de operación correcta
export const ChevronRight = icono(ArrowRight01Icon) // entrar en una carpeta o chat
export const Clock3 = icono(Clock01Icon) // descarga en cola
export const Copy = icono(Copy01Icon) // copiar token y registro
export const Download = icono(Download01Icon) // descargar un archivo
export const ExternalLink = icono(LinkSquare02Icon) // abrir el archivo descargado
export const Eye = icono(ViewIcon) // mostrar el token
export const EyeOff = icono(ViewOffIcon) // ocultar el token
export const FileCheck = icono(FileValidationIcon) // descargas completadas
export const FileDown = icono(FileDownloadIcon) // descargas en curso
export const FileText = icono(File02Icon) // documento genérico
export const Folder = icono(Folder01Icon) // carpeta de descargas
export const Gauge = icono(DashboardSpeed02Icon) // velocidad
export const HardDrive = icono(HardDriveIcon) // unidades de disco
export const HelpCircle = icono(HelpCircleIcon) // modal de confirmación
export const Image = icono(Image02Icon) // foto
export const Inbox = icono(InboxIcon) // bandeja de la escucha
export const Info = icono(InformationCircleIcon) // nota informativa
export const KeyRound = icono(Key02Icon) // credenciales y token
export const Loader2 = icono(Loading03Icon) // espera en curso
export const LogOut = icono(Logout03Icon) // cerrar sesión de Telegram
export const Menu = icono(Menu01Icon) // menú en pantalla pequeña
export const MessageCircle = icono(Message02Icon) // chat vigilado
export const Music = icono(MusicNote01Icon) // audio
export const Pause = icono(PauseIcon) // pausar
export const Phone = icono(SmartPhone01Icon) // número de teléfono
export const Play = icono(PlayIcon) // reanudar
export const Plus = icono(PlusSignIcon) // añadir chat
export const Power = icono(PowerIcon) // apagar el equipo al terminar la cola
export const Radio = icono(RadioTowerIcon) // entrada "Escucha" del menú
export const RefreshCw = icono(Refresh01Icon) // regenerar o recargar
export const RotateCcw = icono(ArrowReloadHorizontalIcon) // reintentar una descarga
export const Save = icono(FloppyDiskIcon) // guardar ajustes
export const ScrollText = icono(Pulse01Icon) // entrada "Logs" del menú
export const Search = icono(Search01Icon) // buscar dentro del registro
export const Send = icono(DeliverySent02Icon) // enviar el token a Telegram
export const Settings2 = icono(Settings02Icon) // entrada "Ajustes" del menú
export const ShieldCheck = icono(ShieldCheckIcon) // verificación en dos pasos
export const Trash2 = icono(Delete02Icon) // eliminar
export const Upload = icono(Upload01Icon) // subida
export const UserCheck = icono(UserCheck01Icon) // usuario con sesión iniciada
export const Video = icono(Video01Icon) // vídeo
export const X = icono(Cancel01Icon) // cerrar
export const Zap = icono(FlashIcon) // actualización disponible
