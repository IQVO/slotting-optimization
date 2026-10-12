import type { SidebarsConfig } from "@docusaurus/plugin-content-docs";

const sidebar: SidebarsConfig = {
  apisidebar: [
    {
      type: "doc",
      id: "api-reference/rest/slotting-optimization-api",
    },
    {
      type: "category",
      label: "Slot plans",
      link: {
        type: "doc",
        id: "api-reference/rest/slot-plans",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/generate-slot-plan",
          label: "Generate a slot plan",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "api-reference/rest/list-slot-plans",
          label: "List slot plans",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/get-slot-plan",
          label: "Get a slot plan",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/approve-slot-plan",
          label: "Approve a Draft plan",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "api-reference/rest/reject-slot-plan",
          label: "Reject a Draft plan",
          className: "api-method post",
        },
      ],
    },
    {
      type: "category",
      label: "Forward slots",
      link: {
        type: "doc",
        id: "api-reference/rest/forward-slots",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/list-forward-slots",
          label: "The current approved forward-slot map",
          className: "api-method get",
        },
      ],
    },
    {
      type: "category",
      label: "Velocity",
      link: {
        type: "doc",
        id: "api-reference/rest/velocity",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/list-sku-velocity",
          label: "SKU velocity over a window",
          className: "api-method get",
        },
      ],
    },
    {
      type: "category",
      label: "Health",
      link: {
        type: "doc",
        id: "api-reference/rest/health",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/healthz",
          label: "Liveness probe",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/readyz",
          label: "Readiness probe",
          className: "api-method get",
        },
      ],
    },
  ],
};

export default sidebar.apisidebar;
