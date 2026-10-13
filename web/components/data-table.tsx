"use client"

import {
  ArrowUpRightIcon,
  CircleCheckIcon,
  Clock3Icon,
  FilterIcon,
} from "lucide-react"

import { formatDateTime } from "@/lib/utils"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "@/components/ui/tabs"
import { useI18n } from "@/i18n/provider"

type DashboardTask = {
  id: number
  module: string
  owner: string
  status: string
  progress: string
  updatedAt: string
}

export function DataTable({ data }: { data: DashboardTask[] }) {
  const t = useI18n()
  return (
    <Tabs
      defaultValue="modules"
      className="w-full flex-col justify-start gap-6 px-4 lg:px-6"
    >
      <div className="flex flex-col gap-4 md:flex-row md:items-center md:justify-between">
        <div>
          <CardTitle className="text-xl">{t("scaffold.moduleBoard")}</CardTitle>
          <CardDescription className="mt-1">
            {t("scaffold.moduleBoardDescription")}
          </CardDescription>
        </div>
        <div className="flex items-center gap-2">
          <Input className="w-full md:w-64" placeholder={t("scaffold.searchModule")} />
          <Button variant="outline">
            <FilterIcon />
            {t("scaffold.filter")}
          </Button>
        </div>
      </div>
      <TabsList className="w-fit">
        <TabsTrigger value="modules">{t("scaffold.moduleList")}</TabsTrigger>
        <TabsTrigger value="milestones">{t("scaffold.milestones")}</TabsTrigger>
      </TabsList>
      <TabsContent value="modules" className="m-0">
        <Card>
          <CardHeader>
            <CardTitle>{t("scaffold.phaseOneModules")}</CardTitle>
            <CardDescription>
              {t("scaffold.phaseOneDescription")}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <div className="overflow-hidden rounded-xl border">
              <Table>
                <TableHeader className="bg-muted/50">
                  <TableRow>
                    <TableHead>{t("scaffold.module")}</TableHead>
                    <TableHead>{t("scaffold.owner")}</TableHead>
                    <TableHead>{t("scaffold.status")}</TableHead>
                    <TableHead>{t("scaffold.progress")}</TableHead>
                    <TableHead className="text-right">{t("scaffold.updatedAt")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {data.map((item) => (
                    <TableRow key={item.id}>
                      <TableCell className="font-medium">{item.module}</TableCell>
                      <TableCell>{item.owner}</TableCell>
                      <TableCell>
                        <Badge variant="outline" className="px-1.5">
                          {item.status === t("scaffold.done") ? (
                            <CircleCheckIcon className="fill-green-500 text-green-500" />
                          ) : (
                            <Clock3Icon className="text-amber-500" />
                          )}
                          {item.status}
                        </Badge>
                      </TableCell>
                      <TableCell>{item.progress}</TableCell>
                      <TableCell className="text-right text-muted-foreground">
                        {formatDateTime(item.updatedAt)}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          </CardContent>
        </Card>
      </TabsContent>
      <TabsContent value="milestones" className="m-0">
        <Card className="border-dashed">
          <CardHeader>
            <CardTitle>{t("scaffold.nextMilestone")}</CardTitle>
            <CardDescription>
              {t("scaffold.nextMilestoneDescription")}
            </CardDescription>
          </CardHeader>
          <CardContent className="grid gap-3">
            {[
              t("scaffold.milestoneLogin"),
              t("scaffold.milestoneForms"),
              t("scaffold.milestoneKnowledge"),
            ].map((item) => (
              <div
                key={item}
                className="flex items-center justify-between rounded-xl border px-4 py-3"
              >
                <span className="text-sm">{item}</span>
                <ArrowUpRightIcon className="size-4 text-muted-foreground" />
              </div>
            ))}
          </CardContent>
        </Card>
      </TabsContent>
    </Tabs>
  )
}
