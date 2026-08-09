import {createTheme} from "@mui/material";

// CutScene theme: cinematic dark, single sharp orange accent.
// No external webfonts — uses refined local/system stacks. A slightly tighter,
// heavier system stack stands in for the display face on headings/wordmark;
// the UI/body and mono stacks use platform defaults.

const displayFont = 'system-ui, "Segoe UI", "Helvetica Neue", Arial, sans-serif';
const bodyFont = 'system-ui, "Segoe UI", "Helvetica Neue", Arial, sans-serif';
const monoFont = 'ui-monospace, "SFMono-Regular", Menlo, Consolas, "Liberation Mono", monospace';

export const theme = createTheme({
  palette: {
    mode: 'dark',
    primary: {
      main: '#ff7300',
      contrastText: '#1a1206',
    },
    secondary: {
      main: '#f50057',
    },
    background: {
      default: '#13151a',
      paper: '#1b1e26',
    },
    text: {
      primary: '#f4f5f8',
      secondary: '#a7adb9', // ~6.8:1 on paper — AA for normal text
      disabled: '#8b919e',  // ~5.2:1 on paper — AA for informational copy
    },
    divider: 'rgba(255,255,255,0.08)',
  },
  typography: {
    fontFamily: bodyFont,
    h1: {fontFamily: displayFont, fontWeight: 700, letterSpacing: '-0.02em'},
    h2: {fontFamily: displayFont, fontWeight: 700, letterSpacing: '-0.02em'},
    h3: {fontFamily: displayFont, fontWeight: 700, letterSpacing: '-0.01em'},
    h4: {fontFamily: displayFont, fontWeight: 600, letterSpacing: '-0.01em'},
    h5: {fontFamily: displayFont, fontWeight: 600},
    h6: {fontFamily: displayFont, fontWeight: 600},
    button: {fontFamily: bodyFont, fontWeight: 600, letterSpacing: '0.01em'},
    caption: {fontFamily: bodyFont},
    overline: {fontFamily: displayFont, fontWeight: 600, letterSpacing: '0.12em'},
  },
  shape: {borderRadius: 10},
  components: {
    MuiCssBaseline: {
      styleOverrides: {
        body: {fontFamily: bodyFont},
        ':root': { '--cs-mono-font': monoFont },
        '::selection': {backgroundColor: 'rgba(255,115,0,0.32)'},
      },
    },
    MuiButton: {
      styleOverrides: {
        root: {
          borderRadius: 999,
          paddingInline: 18,
        },
        contained: {
          boxShadow: 'none',
          '&:hover': {boxShadow: '0 6px 20px -8px rgba(255,115,0,0.6)'},
        },
      },
    },
    MuiCard: {
      styleOverrides: {
        root: {
          backgroundImage: 'none',
          backgroundColor: '#1b1e26',
          border: '1px solid rgba(255,255,255,0.06)',
        },
      },
    },
    MuiSlider: {
      styleOverrides: {
        root: {color: '#ff7300'},
        thumb: {'&:focus-visible': {outline: '2px solid #ffd9b0', outlineOffset: 2}},
      },
    },
    MuiOutlinedInput: {
      styleOverrides: {
        root: {
          '&.Mui-focused .MuiOutlinedInput-notchedOutline': {
            borderColor: '#ff7300',
            borderWidth: 1,
          },
        },
      },
    },
    MuiSelect: {
      styleOverrides: {
        select: {'&:focus-visible': {outline: '2px solid #ffd9b0', outlineOffset: 2}},
      },
    },
  },
});

export const fonts = {displayFont, bodyFont, monoFont};