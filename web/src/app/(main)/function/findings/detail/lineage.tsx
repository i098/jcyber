"use client";

import * as React from "react";

import { ExplorationGraph } from "@/components/exploration-graph";
import { api } from "@/lib/api";
import type { Edge, TaskNode } from "@/lib/types";

// FindingLineageView renders the exploration sub-graph from the task's initial
// node down to this finding's node — the same 攻击链路图 canvas as the task graph,
// scoped to just this finding's lineage.
export function FindingLineageView({ findingId }: { findingId: string }) {
  const [nodes, setNodes] = React.useState<TaskNode[]>([]);
  const [edges, setEdges] = React.useState<Edge[]>([]);
  const [loaded, setLoaded] = React.useState(false);

  React.useEffect(() => {
    let alive = true;
    api
      .findingLineage(findingId)
      .then((g) => {
        if (!alive) return;
        setNodes(g.nodes ?? []);
        setEdges(g.edges ?? []);
      })
      .catch(() => {})
      .finally(() => {
        if (alive) setLoaded(true);
      });
    return () => {
      alive = false;
    };
  }, [findingId]);

  if (loaded && nodes.length === 0) {
    return (
      <p className="text-muted-foreground p-6 text-sm">
        No exploration path to display (this Vulnerability has no linked exploration node, or its Task has been deleted).
      </p>
    );
  }

  return (
    <ExplorationGraph
      nodes={nodes}
      edges={edges}
      className="h-[68vh]"
      emptyHint={loaded ? "No exploration path" : "Loading..."}
    />
  );
}
