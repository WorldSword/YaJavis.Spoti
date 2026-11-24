// Build with the pinned yt-dlp/ejs checkout. See THIRD_PARTY.md.
import { generate } from "astring";
import { parse } from "meriyah";
import { extract } from "./src/yt/solver/nsig.ts";
import { setupNodes } from "./src/yt/solver/setup.ts";

type Node = any;

const keys: Record<string, string[]> = {
  Program: ["body"],
  BlockStatement: ["body"],
  SequenceExpression: ["expressions"],
  ExpressionStatement: ["expression"],
  BinaryExpression: ["left", "right"],
  LogicalExpression: ["left", "right"],
  AssignmentExpression: ["left", "right"],
  VariableDeclaration: ["declarations"],
  VariableDeclarator: ["id", "init"],
  IfStatement: ["test", "consequent", "alternate"],
  ConditionalExpression: ["test", "consequent", "alternate"],
  ReturnStatement: ["argument"],
  ThrowStatement: ["argument"],
  UnaryExpression: ["argument"],
  UpdateExpression: ["argument"],
  CallExpression: ["callee", "arguments"],
  NewExpression: ["callee", "arguments"],
  MemberExpression: ["object", "property"],
  ObjectExpression: ["properties"],
  ArrayExpression: ["elements"],
  Property: ["key", "value"],
  WhileStatement: ["test", "body"],
  ForStatement: ["init", "test", "update", "body"],
  ForOfStatement: ["left", "right", "body"],
  ForInStatement: ["left", "right", "body"],
  SwitchStatement: ["discriminant", "cases"],
  SwitchCase: ["test", "consequent"],
  TryStatement: ["block", "handler", "finalizer"],
  Literal: [],
  ThisExpression: [],
  EmptyStatement: [],
};

function children(node: Node, visit: (child: Node) => void) {
  const fields = keys[node.type] ?? Object.keys(node);
  for (let i = 0; i < fields.length; i++) {
    const value = node[fields[i]];
    if (Array.isArray(value)) {
      for (let j = 0; j < value.length; j++) {
        if (value[j]?.type) visit(value[j]);
      }
    } else if (value?.type) visit(value);
  }
}

function root(node: Node, namespace: string): string | undefined {
  if (node.type === "Identifier") return node.name;
  if (node.type !== "MemberExpression") return;
  if (!node.computed && node.object.type === "Identifier" && node.object.name === namespace)
    return namespace + "." + node.property.name;
  return root(node.object, namespace);
}

function definitions(node: Node, namespace: string): string[] {
  if (node.type === "VariableDeclaration")
    return node.declarations.filter((decl: Node) => decl.id.type === "Identifier").map((decl: Node) => decl.id.name);
  if (node.type === "FunctionDeclaration" || node.type === "ClassDeclaration")
    return node.id ? [node.id.name] : [];
  const names = [];
  let expr = node.expression;
  while (expr?.type === "AssignmentExpression") {
    const name = root(expr.left, namespace);
    if (name) names.push(name);
    expr = expr.right;
  }
  return names;
}

function addBinding(pattern: Node, names: Set<string>) {
  switch (pattern.type) {
    case "Identifier": names.add(pattern.name); break;
    case "AssignmentPattern": addBinding(pattern.left, names); break;
    case "RestElement": addBinding(pattern.argument, names); break;
    case "ArrayPattern":
      for (const element of pattern.elements) if (element) addBinding(element, names);
      break;
    case "ObjectPattern":
      for (const property of pattern.properties) addBinding(property.value ?? property, names);
      break;
  }
}

function dependencies(node: Node, namespace: string): string[] {
  const globals = new Set<string>();
  let used = globals;
  let locals: Set<string> | undefined;
  const visit = (node: Node) => {
    switch (node.type) {
      case "Identifier":
        used.add(node.name);
        return;
      case "MemberExpression":
        if (!node.computed) {
          if (node.object.type === "Identifier" && node.object.name === namespace)
            used.add(namespace + "." + node.property.name);
          else visit(node.object);
          return;
        }
        break;
      case "Property":
        if (!node.computed) { visit(node.value); return; }
        break;
      case "VariableDeclaration":
        if (locals && node.kind === "var") {
          for (const decl of node.declarations) addBinding(decl.id, locals);
        }
        break;
      case "FunctionDeclaration":
      case "FunctionExpression":
      case "ArrowFunctionExpression": {
        const outer = used;
        const outerLocals = locals;
        used = new Set();
        locals = new Set();
        if (node.id) locals.add(node.id.name);
        for (const param of node.params) {
          addBinding(param, locals);
          visit(param);
        }
        const body = node.body.type === "BlockStatement" ? node.body.body : [];
        for (const statement of body) {
          if (statement.type === "FunctionDeclaration" || statement.type === "ClassDeclaration")
            locals.add(statement.id.name);
          if (statement.type === "VariableDeclaration") {
            for (const decl of statement.declarations) addBinding(decl.id, locals);
          }
        }
        visit(node.body);
        for (const name of used) {
          if (!locals.has(name)) outer.add(name);
        }
        used = outer;
        locals = outerLocals;
        return;
      }
    }
    children(node, visit);
  };
  visit(node);
  return [...globals];
}

export function begin(namespace: string, skip: boolean) {
  const pieces: { code: string; defines: string[]; uses: string[]; solver: string | null; hoisted: boolean; vars: string[] }[] = [];
  let first = true;
  let strict = false;
  function add(source: string) {
    let statements: Node[];
    try {
      statements = parse(source, { ranges: true }).body;
    } catch {
      return false;
    }
    for (const statement of statements) {
      if (first) {
        first = false;
        strict = statement.directive === "use strict" || statement.expression?.value === "use strict";
        if (skip) continue;
      }
      if (statement.type === "ExpressionStatement" && !["AssignmentExpression", "Literal"].includes(statement.expression.type))
        continue;
      const code = source.slice(statement.start, statement.end);
      const solver = code.includes("alr") ? extract(statement) : null;
      const defines = definitions(statement, namespace);
      pieces.push({
        code,
        defines,
        uses: dependencies(statement, namespace),
        solver: solver ? generate(solver) : null,
        hoisted: statement.type === "FunctionDeclaration",
        vars: statement.type === "VariableDeclaration" && statement.kind === "var" ? defines : [],
      });
    }
    return true;
  }
  function finish() {
    const byName = new Map<string, number[]>();
    const wanted = new Set<number>();
    const seen = new Set<string>();
    const solvers: string[] = [];
    for (const [index, piece] of pieces.entries()) {
      if (piece.solver) solvers.push(piece.solver);
      if (piece.solver || !piece.defines.length) wanted.add(index);
      for (const name of piece.defines) {
        const list = byName.get(name);
        if (list) list.push(index);
        else byName.set(name, [index]);
      }
    }
    if (!solvers.length) throw Error("No compatible player URL helper");
    for (const index of wanted) {
      for (const name of pieces[index].uses) {
        if (seen.has(name)) continue;
        seen.add(name);
        for (const dependency of byName.get(name) ?? []) wanted.add(dependency);
      }
    }
    const keep = pieces.filter((_, index) => wanted.has(index));
    const chunks = [generate({ type: "Program", body: setupNodes, sourceType: "script" })];
    if (namespace) chunks.push("var " + namespace + " = {};");
    const vars = keep.flatMap((piece) => piece.vars);
    if (vars.length) chunks.push("var " + [...new Set(vars)].join(",") + ";");
    const prefix = strict ? '"use strict";\n' : "";
    for (const functions of [true, false]) {
      let chunk = prefix;
      for (const piece of keep) {
        if (piece.hoisted !== functions) continue;
        chunk += piece.code + ";\n";
        if (chunk.length >= 96 * 1024) {
          chunks.push(chunk);
          chunk = prefix;
        }
      }
      if (chunk !== prefix) chunks.push(chunk);
    }
    chunks.push(prefix + "globalThis.__solve = " + "(function () {\n" +
      "const functions = [" + solvers.join(",") + "];\nconst solve = (" +
      function solve() {
        return (kind: "n" | "sig", value: string) => {
          const results = new Set<string>();
          for (const transform of functions) {
            try {
              const result = transform({ [kind]: value })[kind];
              if (typeof result === "string" && result) results.add(result);
            } catch {}
          }
          if (results.size !== 1) throw Error("No unique player URL result");
          return results.values().next().value;
        };
      }.toString() +
      ");\nreturn solve();\n})();");
    return chunks;
  }
  return { add, finish };
}
