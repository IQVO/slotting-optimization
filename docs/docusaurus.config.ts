import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';
import type * as OpenApiPlugin from 'docusaurus-plugin-openapi-docs';

const config: Config = {
  title: 'Slotting Optimization',
  tagline:
    'Reviewable plans that decide which SKUs get a forward pick slot, and which slot, with a human approval. A WMS-tier Supporting context.',
  favicon: 'img/favicon.svg',

  future: {
    v4: true,
    faster: true,
  },

  url: 'https://iqvo.github.io',
  baseUrl: '/slotting-optimization/',

  organizationName: 'IQVO',
  projectName: 'slotting-optimization',
  deploymentBranch: 'gh-pages',
  trailingSlash: false,

  onBrokenLinks: 'throw',
  onBrokenAnchors: 'throw',

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  markdown: {
    mermaid: true,
    hooks: {
      onBrokenMarkdownLinks: 'throw',
    },
  },

  presets: [
    [
      'classic',
      {
        docs: {
          sidebarPath: './sidebars.ts',
          editUrl:
            'https://github.com/IQVO/slotting-optimization/tree/main/docs/docs/',
          docItemComponent: '@theme/ApiItem',
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  plugins: [
    // The ADRs stay in docs/adr/*.md (other repos link to those paths); this
    // second docs instance serves them in place under /docs/adr.
    [
      '@docusaurus/plugin-content-docs',
      {
        id: 'adr',
        path: 'adr',
        routeBasePath: 'docs/adr',
        sidebarPath: './sidebarsAdr.ts',
        // Keep the 0001- prefix in URLs/ids so they match the file names.
        numberPrefixParser: false,
        editUrl: 'https://github.com/IQVO/slotting-optimization/tree/main/docs/adr/',
      },
    ],
    [
      'docusaurus-plugin-openapi-docs',
      {
        id: 'openapi',
        docsPluginId: 'classic',
        config: {
          'slotting-optimization': {
            // The single source of truth: the same Spectral-linted spec the
            // service ships and CI gates on. Never hand-transcribed here.
            specPath: '../apis/openapi.yaml',
            outputDir: 'docs/api-reference/rest',
            sidebarOptions: {
              groupPathsBy: 'tag',
              categoryLinkSource: 'tag',
            },
            hideSendButton: true,
          } satisfies OpenApiPlugin.Options,
        },
      },
    ],
  ],

  themes: ['docusaurus-theme-openapi-docs', '@docusaurus/theme-mermaid'],

  themeConfig: {
    colorMode: {
      respectPrefersColorScheme: true,
    },
    navbar: {
      title: 'Slotting Optimization',
      logo: {
        alt: 'Slotting Optimization',
        src: 'img/logo.svg',
      },
      items: [
        {
          type: 'docSidebar',
          sidebarId: 'docsSidebar',
          position: 'left',
          label: 'Documentation',
        },
        {
          to: '/docs/api-reference',
          label: 'API Reference',
          position: 'left',
        },
        {
          to: '/docs/adr',
          label: 'ADRs',
          position: 'left',
        },
        {
          href: 'https://github.com/IQVO/slotting-optimization',
          label: 'GitHub',
          position: 'right',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Documentation',
          items: [
            {label: 'Introduction', to: '/docs/overview/introduction'},
            {label: 'Quickstart', to: '/docs/overview/quickstart'},
            {label: 'Runbook', to: '/docs/operations/runbook'},
            {label: 'API Reference', to: '/docs/api-reference'},
            {label: 'Architecture decisions', to: '/docs/adr'},
          ],
        },
        {
          title: 'Neighbouring contexts',
          items: [
            {label: 'order-management', href: 'https://github.com/IQVO/order-management'},
            {label: 'product-master', href: 'https://github.com/IQVO/product-master'},
            {label: 'facility-layout', href: 'https://github.com/IQVO/facility-layout'},
          ],
        },
        {
          title: 'Source',
          items: [
            {label: 'GitHub repository', href: 'https://github.com/IQVO/slotting-optimization'},
            {label: 'OpenAPI spec', href: 'https://raw.githubusercontent.com/IQVO/slotting-optimization/main/apis/openapi.yaml'},
            {label: 'AsyncAPI spec', href: 'https://raw.githubusercontent.com/IQVO/slotting-optimization/main/apis/asyncapi.yaml'},
          ],
        },
      ],
      copyright: `Slotting Optimization — a warehouse-systems bounded context. Built ${new Date().getFullYear()}.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['bash', 'go', 'json', 'yaml', 'sql'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
