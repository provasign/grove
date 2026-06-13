<?php
// boot.php — auto_prepend bootstrap for the PHP dynamic call-edge oracle.
//
// Registers a shutdown handler that reflects every in-repo class method and
// user function loaded during the run and writes a name → (file, line,
// display-name) map. Joined with the xdebug function trace by the Go side,
// this yields caller→callee edges between in-repo declarations — the same
// dynamic, exact-but-partial oracle as Python's pytruth (recall is the
// headline; precision a lower bound, since a correct static edge on an
// untested path is absent from the trace).
//
// PHPTRUTH_ROOT  — repo root; only declarations under it are recorded.
// PHPTRUTH_REFL  — output path for the JSON reflection map.

register_shutdown_function(function () {
    $root = getenv('PHPTRUTH_ROOT');
    $out = getenv('PHPTRUTH_REFL');
    if ($root === false || $out === false) {
        return;
    }
    // Reflection's getFileName returns realpaths (on macOS /tmp resolves
    // to /private/tmp), so canonicalize the root the same way.
    $real = realpath($root);
    if ($real !== false) {
        $root = $real;
    }
    $root = rtrim(str_replace('\\', '/', $root), '/') . '/';
    $rel = function ($file) use ($root) {
        if ($file === false) {
            return null;
        }
        $file = str_replace('\\', '/', $file);
        if (strpos($file, $root) !== 0) {
            return null;
        }
        $r = substr($file, strlen($root));
        // Exclude installed dependencies; only first-party code is truth.
        if (strpos($r, 'vendor/') === 0) {
            return null;
        }
        return $r;
    };
    $shortClass = function ($fqn) {
        $p = strrpos($fqn, '\\');
        return $p === false ? $fqn : substr($fqn, $p + 1);
    };

    $map = [];
    foreach (get_declared_classes() as $cls) {
        try {
            $rc = new ReflectionClass($cls);
        } catch (\Throwable $e) {
            continue;
        }
        // All methods, inherited included: xdebug names a call by the
        // runtime class ("Child->m") but the declaration lives in the
        // parent, so every accessible spelling must map to the declaring
        // method's source location.
        foreach ($rc->getMethods() as $m) {
            $decl = $m->getDeclaringClass();
            $r = $rel($m->getFileName());
            if ($r === null) {
                continue;
            }
            $entry = ['file' => $r, 'line' => $m->getStartLine(), 'name' => $shortClass($decl->getName()) . '.' . $m->getName()];
            // xdebug spells calls "Class->m" (instance) and "Class::m"
            // (static); record both for the runtime class.
            $map[$cls . '->' . $m->getName()] = $entry;
            $map[$cls . '::' . $m->getName()] = $entry;
        }
    }
    foreach (get_defined_functions()['user'] as $fn) {
        try {
            $rf = new ReflectionFunction($fn);
        } catch (\Throwable $e) {
            continue;
        }
        $r = $rel($rf->getFileName());
        if ($r === null) {
            continue;
        }
        $map[$fn] = ['file' => $r, 'line' => $rf->getStartLine(), 'name' => $shortClass($fn)];
    }
    file_put_contents($out, json_encode($map));
});
