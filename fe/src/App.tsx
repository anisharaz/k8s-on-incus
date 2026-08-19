import { Navigate, Route, Routes } from "react-router";
import { AuthGate } from "./components/AuthGate";
import { ProtectedLayout } from "./components/ProtectedLayout";
import { TerminalOverlay } from "./components/TerminalOverlay";
import { Clusters } from "./pages/Clusters";
import { ClusterDetail } from "./pages/ClusterDetail";
import { Networks } from "./pages/Networks";
import { Users } from "./pages/Users";
import { Settings } from "./pages/Settings";
import { TerminalSessionsProvider } from "./context";

function App() {
  return (
    <TerminalSessionsProvider>
      <Routes>
        <Route path="/login" element={<AuthGate />} />
        {/* The visible terminal UI is TerminalOverlay, mounted once below
            and rendered on top of everything else while this route is
            active — not this route's own element — so navigating away
            doesn't unmount it and kill every open session (see
            TerminalOverlay and TerminalSessionsProvider). This route only
            needs to exist so the path doesn't fall through to
            ProtectedLayout's catch-all below. */}
        <Route path="/terminal/:clusterId/:nodeId" element={null} />
        <Route element={<ProtectedLayout />}>
          <Route path="/" element={<Navigate to="/clusters" replace />} />
          <Route path="/clusters" element={<Clusters />} />
          <Route path="/clusters/:clusterId" element={<ClusterDetail />} />
          <Route path="/networks" element={<Networks />} />
          <Route path="/users" element={<Users />} />
          <Route path="/settings" element={<Settings />} />
          <Route path="*" element={<Navigate to="/clusters" replace />} />
        </Route>
      </Routes>
      <TerminalOverlay />
    </TerminalSessionsProvider>
  );
}

export default App;
