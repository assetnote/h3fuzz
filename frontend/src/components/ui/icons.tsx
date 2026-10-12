import {
  ActivityLogIcon,
  ArchiveIcon,
  ArrowTopRightIcon,
  CheckCircledIcon,
  CheckIcon,
  ChevronDownIcon,
  ClockIcon,
  CodeIcon,
  CopyIcon,
  Cross2Icon,
  DownloadIcon,
  DotsHorizontalIcon,
  DrawingPinIcon,
  ExclamationTriangleIcon,
  FileTextIcon,
  GearIcon,
  GlobeIcon,
  InfoCircledIcon,
  LightningBoltIcon,
  MagnifyingGlassIcon,
  MixerHorizontalIcon,
  PauseIcon,
  PlayIcon,
  PlusIcon,
  ReloadIcon,
  RocketIcon,
  RowsIcon,
  StackIcon,
  StopIcon,
  StopwatchIcon,
  TrashIcon,
} from '@radix-ui/react-icons';
import { forwardRef, type ComponentProps, type SVGProps } from 'react';

export type IconProps = Omit<SVGProps<SVGSVGElement>, 'stroke'> & {
  size?: string | number;
  stroke?: string | number;
};

type RadixIcon = typeof CheckIcon;

function compatIcon(RadixComponent: RadixIcon) {
  return forwardRef<SVGSVGElement, IconProps>(function CompatIcon({ size = 16, stroke, ...props }, ref) {
    void stroke;
    return <RadixComponent {...(props as ComponentProps<typeof CheckIcon>)} ref={ref} width={size} height={size} />;
  });
}

export const IconAdjustments = compatIcon(MixerHorizontalIcon);
export const IconAdjustmentsHorizontal = compatIcon(MixerHorizontalIcon);
export const IconAlertTriangle = compatIcon(ExclamationTriangleIcon);
export const IconArrowUpRight = compatIcon(ArrowTopRightIcon);
export const IconBolt = compatIcon(LightningBoltIcon);
export const IconBraces = compatIcon(CodeIcon);
export const IconBrandSpeedtest = compatIcon(StopwatchIcon);
export const IconCheck = compatIcon(CheckIcon);
export const IconChevronDown = compatIcon(ChevronDownIcon);
export const IconClock = compatIcon(ClockIcon);
export const IconCopy = compatIcon(CopyIcon);
export const IconDatabaseExport = compatIcon(ArchiveIcon);
export const IconDeviceFloppy = compatIcon(ArchiveIcon);
export const IconDownload = compatIcon(DownloadIcon);
export const IconDots = compatIcon(DotsHorizontalIcon);
export const IconFileCode = compatIcon(CodeIcon);
export const IconFileText = compatIcon(FileTextIcon);
export const IconPin = compatIcon(DrawingPinIcon);
export const IconFilter = compatIcon(MixerHorizontalIcon);
export const IconInfoCircle = compatIcon(InfoCircledIcon);
export const IconLanguage = compatIcon(GlobeIcon);
export const IconPlayerPause = compatIcon(PauseIcon);
export const IconPlayerPlay = compatIcon(PlayIcon);
export const IconPlayerStop = compatIcon(StopIcon);
export const IconPlus = compatIcon(PlusIcon);
export const IconRefresh = compatIcon(ReloadIcon);
export const IconRocket = compatIcon(RocketIcon);
export const IconRows = compatIcon(RowsIcon);
export const IconRoute = compatIcon(ActivityLogIcon);
export const IconSearch = compatIcon(MagnifyingGlassIcon);
export const IconStack = compatIcon(StackIcon);
export const IconSettings = compatIcon(GearIcon);
export const IconShieldCheck = compatIcon(CheckCircledIcon);
export const IconTerminal2 = compatIcon(CodeIcon);
export const IconTrash = compatIcon(TrashIcon);
export const IconWorld = compatIcon(GlobeIcon);
export const IconX = compatIcon(Cross2Icon);
export const IconZoomCheck = compatIcon(CheckCircledIcon);
