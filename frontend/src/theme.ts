import { Button, Tooltip, createTheme, type MantineColorsTuple } from '@mantine/core'

// Cyanotype palette, from the approved design board.
// Index 6 of each tuple is the approved value. Index 4 of prussian and carmine
// is the dark-mode lift (see the board's derived values).

const prussian: MantineColorsTuple = [
  '#EEF4F9', '#DCE8F2', '#BCD5EA', '#9CC3E0', '#6FA8D6',
  '#3F7FB3', '#1F4E79', '#193F62', '#13314D', '#0C2238',
]

const carmine: MantineColorsTuple = [
  '#FBEFF4', '#F5D6E2', '#EDB3C9', '#E485A8', '#E0608F',
  '#C84A7A', '#B0306A', '#92285A', '#75204A', '#581839',
]

// Oxide: the error token. Index 6 is the light-mode value (#A8402A, about 5.8:1 on sheet).
// Index 4 is the dark-mode value (#E07A62, about 5.8:1 on the dark body).
const oxide: MantineColorsTuple = [
  '#FBEFEB', '#F5D9D0', '#EBB7A6', '#E09A85', '#E07A62',
  '#C25C42', '#A8402A', '#8C3420', '#72291A', '#571E13',
]

// Light surfaces and rules. gray[3] is the approved Rule colour (#C5D0D8).
const gray: MantineColorsTuple = [
  '#F8FAFB', '#EEF2F5', '#E0E7ED', '#C5D0D8', '#A9B8C5',
  '#8798AA', '#64788C', '#4A5E72', '#33475A', '#1F2F40',
]

// Dark surfaces. dark[7] is the dark body (#0B1B2E), dark[6] the sheet (#12263D),
// dark[4] the dark rule (#2A4560), dark[0] the primary text (#E6EEF5).
const dark: MantineColorsTuple = [
  '#E6EEF5', '#B4C4D4', '#8FA3B8', '#6B8299', '#2A4560',
  '#1B3350', '#12263D', '#0B1B2E', '#081524', '#050E19',
]

const fontFamily = "'Hanken Grotesk', system-ui, -apple-system, 'Segoe UI', sans-serif"
const fontFamilyMonospace = "'Martian Mono', ui-monospace, Menlo, Consolas, monospace"

export const theme = createTheme({
  primaryColor: 'prussian',
  primaryShade: { light: 6, dark: 4 },
  // `red` is remapped to oxide so Mantine's own error styles (field errors,
  // required asterisks) use the error token. `oxide` is the named alias.
  colors: { prussian, carmine, gray, dark, oxide, red: oxide },
  white: '#F8FAFB',
  black: '#0F2A4A',
  fontFamily,
  fontFamilyMonospace,
  headings: { fontFamily, fontWeight: '700' },
  defaultRadius: 'sm',
  radius: { xs: '3px', sm: '4px', md: '6px' },
  // Flat surfaces. The only shadow in the product is the light-table sheet, set per component.
  shadows: { xs: 'none', sm: 'none', md: 'none', lg: 'none', xl: 'none' },
  components: {
    Button: Button.extend({
      defaultProps: { radius: 'sm' },
    }),
    Tooltip: Tooltip.extend({
      defaultProps: { withArrow: true },
    }),
  },
})
