"use client";

import React, { useEffect, useState } from "react";
import { Card, CardHeader, CardTitle, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
} from "@/components/ui/select";
import {
  useGetDbConfigQuery,
  useUpdateDbConfigMutation,
} from "@/lib/store/apis/dbConfig"; 
import { toast } from "sonner";

const DbConfigCard = () => {
  const { data, isLoading, error } = useGetDbConfigQuery();
  const [updateDbConfig, { isLoading: isUpdating }] =
    useUpdateDbConfigMutation();

  const [dbType, setDbType] = useState<"sqlite" | "postgres">("sqlite");
  const [sqlitePath, setSqlitePath] = useState("./bifrost.db");
  const [pgConfig, setPgConfig] = useState({
    host: "",
    port: "",
    user: "",
    password: "",
    dbName: "",
    sslmode: "disable",
  });

  // Populate state from API data
  useEffect(() => {
    if (data) {
      if (data.type === "sqlite") {
        setDbType("sqlite");
        setSqlitePath((data.config as any)?.path || "./bifrost.db");
      } else if (data.type === "postgres") {
        setDbType("postgres");
        setPgConfig({
          host: (data.config as any)?.host || "",
          port: String((data.config as any)?.port || ""),
          user: (data.config as any)?.user || "",
          password: (data.config as any)?.password || "",
          dbName: (data.config as any)?.dbName || "",
          sslmode: (data.config as any)?.sslMode || "disable",
        });
      }
    }
  }, [data]);

  const handlePgChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    setPgConfig({ ...pgConfig, [e.target.name]: e.target.value });
  };

  const handleSave = async () => {
    try {
      const payload =
        dbType === "sqlite"
          ? {
              enabled: true,
              type: "sqlite",
              config: { path: sqlitePath },
            }
          : {
              enabled: true,
              type: "postgres",
              config: {
                host: pgConfig.host,
                port: Number(pgConfig.port),
                user: pgConfig.user,
                password: pgConfig.password,
                dbName: pgConfig.dbName,
                sslMode: pgConfig.sslmode,
              },
            };
      await updateDbConfig(payload).unwrap();

      toast.success("Database config updated successfully.");
      // TODO: trigger migration here if needed
    } catch (err: any) {
      console.error("Failed to update config:", err);
      toast.error("Failed to update config");
    }
  };

  if (isLoading) return <p>Loading config...</p>;
  if (error) return <p>Failed to load config</p>;

  return (
    <Card className="rounded-2xl shadow-md">
      <CardHeader>
        <CardTitle>Database Configuration</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        {/* DB Type Selection */}
        <div>
          <Label className="mb-2">Database Type</Label>
          <Select
            value={dbType}
            onValueChange={(val) => setDbType(val as "sqlite" | "postgres")}
          >
            <SelectTrigger>
              <SelectValue placeholder="Choose database type" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="sqlite">SQLite</SelectItem>
              <SelectItem value="postgres">Postgres</SelectItem>
            </SelectContent>
          </Select>
        </div>

        {/* SQLite */}
        {dbType === "sqlite" && (
          <div className="mt-4">
            <Label className="mb-2">SQLite Path</Label>
            <Input
              value={sqlitePath}
              onChange={(e) => setSqlitePath(e.target.value)}
              placeholder="./bifrost.db"
            />
          </div>
        )}

        {/* Postgres */}
        {dbType === "postgres" && (
          <>
            {["host", "port", "user", "password", "dbName"].map((field) => (
              <div className="mt-4" key={field}>
                <Label className="mb-2">{field}</Label>
                <Input
                  type={field === "password" ? "password" : "text"}
                  name={field}
                  value={(pgConfig as any)[field]}
                  onChange={handlePgChange}
                  placeholder={field}
                />
              </div>
            ))}

            <div className="mt-4">
              <Label className="mb-2">SSL Mode</Label>
              <Select
                value={pgConfig.sslmode}
                onValueChange={(val) =>
                  setPgConfig({ ...pgConfig, sslmode: val })
                }
              >
                <SelectTrigger>
                  <SelectValue placeholder="Select SSL Mode" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="disable">disable</SelectItem>
                  <SelectItem value="prefer">prefer</SelectItem>
                  <SelectItem value="require">require</SelectItem>
                  <SelectItem value="verify-ca">verify-ca</SelectItem>
                  <SelectItem value="verify-full">verify-full</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </>
        )}

        <Button
          className="w-full mt-6"
          onClick={handleSave}
          disabled={isUpdating}
        >
          {isUpdating ? "Saving..." : "Save Database Config"}
        </Button>
      </CardContent>
    </Card>
  );
};

export default DbConfigCard;
